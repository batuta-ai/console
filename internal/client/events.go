package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/coder/websocket"
)

type EventEnvelope struct {
	Type   string         `json:"type"`
	Events []domain.Event `json:"events,omitempty"`
	Event  *domain.Event  `json:"event,omitempty"`
}

// EventStream owns one attachment. Cancellation detaches even while idle.
// Closing it never reconnects or resets the caller's cursor.
type EventStream struct {
	conn *websocket.Conn
	ctx  context.Context
	stop func() bool
}

func (s *EventStream) Close() error { s.stop(); return s.conn.CloseNow() }
func (s *EventStream) Read(ctx context.Context) (EventEnvelope, error) {
	var out EventEnvelope
	readCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer cancel()
	defer stop()
	kind, data, err := s.conn.Read(readCtx)
	if err != nil {
		if s.ctx.Err() != nil {
			err = s.ctx.Err()
		}
		// A pre-canceled read can fail before websocket closes the connection.
		return out, &OperationError{Effect: EffectReadOnly, Cause: errors.Join(err, s.Close())}
	}
	if kind != websocket.MessageText {
		return out, &OperationError{Effect: EffectReadOnly, Cause: fmt.Errorf("invalid event message type")}
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, &OperationError{Effect: EffectReadOnly, Cause: err}
	}
	if (out.Type == "snapshot" && out.Event == nil && len(out.Events) <= 200) || (out.Type == "event" && out.Event != nil && len(out.Events) == 0) {
		return out, nil
	}
	return EventEnvelope{}, &OperationError{Effect: EffectReadOnly, Cause: fmt.Errorf("invalid event envelope")}
}
func (c *Client) DialEvents(ctx context.Context, cursor *int64) (*EventStream, error) {
	fail := func(status int, err error) (*EventStream, error) {
		return nil, &OperationError{Status: status, Effect: EffectReadOnly, Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(0, err)
	}
	if cursor != nil && *cursor < 0 {
		return fail(0, domain.ErrInvalidReference)
	}
	u, err := url.Parse(c.Base + "/v0/events")
	if err != nil {
		return fail(0, err)
	}
	q := u.Query()
	if cursor != nil {
		q.Set("cursor", strconv.FormatInt(*cursor, 10))
	}
	u.RawQuery = q.Encode()
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	handshake := &eventHandshakeTransport{base: transport}
	hc.Transport = handshake
	conn, res, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: &hc, HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}}})
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		if handshake.failure != nil {
			handshake.failure.Cause = errors.Join(err, handshake.failure.Cause)
			return nil, handshake.failure
		}
		return fail(status, err)
	}

	conn.SetReadLimit(maxOperationBytes)
	stream := &EventStream{conn: conn, ctx: ctx}
	stream.stop = context.AfterFunc(ctx, func() { conn.CloseNow() })
	return stream, nil
}

// Capture the bounded HTTP error before websocket.Dial truncates it to 1024 bytes.
type eventHandshakeTransport struct {
	base    http.RoundTripper
	failure *OperationError
}

func (t *eventHandshakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || res.StatusCode == http.StatusSwitchingProtocols {
		return res, err
	}
	failure := &OperationError{Status: res.StatusCode, Effect: EffectReadOnly}
	data, readErr := io.ReadAll(io.LimitReader(res.Body, maxOperationBytes+1))
	closeErr := res.Body.Close()
	failure.Cause = errors.Join(readErr, closeErr)
	if len(data) > maxOperationBytes {
		failure.Cause = errors.Join(failure.Cause, fmt.Errorf("response exceeds limit"))
		data = nil
	} else if readErr == nil {
		var wire struct {
			Code    string `json:"error"`
			Message string `json:"message"`
		}
		if decodeErr := json.Unmarshal(data, &wire); decodeErr != nil {
			failure.Cause = errors.Join(failure.Cause, decodeErr)
		} else {
			failure.Code = wire.Code
			failure.Message = wire.Message
		}
	}
	t.failure = failure
	res.Body = io.NopCloser(bytes.NewReader(data))
	return res, nil
}
