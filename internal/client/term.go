package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

const TerminalProtocol = "console-terminal-v1"
const terminalMessageLimit = 64 << 10

var ErrUnsupportedTerminalProtocol = errors.New("unsupported_terminal_protocol")
var ErrInvalidTerminalGeometry = errors.New("invalid_terminal_geometry")
var ErrTerminalInputTooLarge = errors.New("terminal_input_overflow")

type TerminalEvent struct {
	Data []byte
	Cols int
	Rows int
}

// Terminal is an explicitly negotiated v1 attachment. It never retries input or
// reacquires control. Callers own their reader and cancel it by closing Terminal.
type Terminal struct {
	conn      *websocket.Conn
	writeGate chan struct{}
	geometry  bool
}

// DialTerminal retains the geometry-free legacy wire for existing consumers.
func (c *Client) DialTerminal(ctx context.Context, sessionID, clientID string) (*websocket.Conn, error) {
	return c.dialTerminal(ctx, sessionID, clientID, nil)
}

func (c *Client) dialTerminal(ctx context.Context, sessionID, clientID string, protocols []string) (*websocket.Conn, error) {
	base := strings.Replace(c.Base, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	u, err := url.Parse(base + "/v0/sessions/" + url.PathEscape(sessionID) + "/terminal")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("token", c.Token)
	q.Set("client", clientID)
	u.RawQuery = q.Encode()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient: c.HTTP, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.Token}}, Subprotocols: protocols,
	})
	if err == nil {
		conn.SetReadLimit(terminalMessageLimit)
	}
	return conn, err
}

func (c *Client) DialTerminalV1(ctx context.Context, sessionID, clientID string) (*Terminal, error) {
	conn, err := c.dialTerminal(ctx, sessionID, clientID, []string{TerminalProtocol})
	if err != nil {
		return nil, err
	}
	if conn.Subprotocol() != TerminalProtocol {
		_ = conn.CloseNow()
		return nil, ErrUnsupportedTerminalProtocol
	}
	return &Terminal{conn: conn, writeGate: make(chan struct{}, 1)}, nil
}

// Read has one caller, like websocket.Conn.Read. A stream cannot become truthful
// from an unrelated HTTP snapshot; its first event must be ordered geometry.
func (t *Terminal) Read(ctx context.Context) (TerminalEvent, error) {
	typ, data, err := t.conn.Read(ctx)
	if err != nil {
		return TerminalEvent{}, err
	}
	if typ == websocket.MessageBinary && t.geometry {
		return TerminalEvent{Data: data}, nil
	}
	if typ == websocket.MessageText {
		e, err := decodeTerminalGeometry(data)
		if err == nil {
			t.geometry = true
			return e, nil
		}
	}
	_ = t.conn.Close(websocket.StatusPolicyViolation, "invalid_terminal_geometry")
	return TerminalEvent{}, ErrInvalidTerminalGeometry
}

func decodeTerminalGeometry(data []byte) (TerminalEvent, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return TerminalEvent{}, ErrInvalidTerminalGeometry
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return TerminalEvent{}, ErrInvalidTerminalGeometry
		}
		name, ok := key.(string)
		if !ok {
			return TerminalEvent{}, ErrInvalidTerminalGeometry
		}
		if _, exists := values[name]; exists {
			return TerminalEvent{}, ErrInvalidTerminalGeometry
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return TerminalEvent{}, ErrInvalidTerminalGeometry
		}
		values[name] = value
	}
	if _, err = d.Token(); err != nil {
		return TerminalEvent{}, ErrInvalidTerminalGeometry
	}
	if _, err = d.Token(); err != io.EOF {
		return TerminalEvent{}, ErrInvalidTerminalGeometry
	}
	var typ string
	var cols, rows int
	if len(values) != 3 || json.Unmarshal(values["type"], &typ) != nil || typ != "geometry" || json.Unmarshal(values["cols"], &cols) != nil || json.Unmarshal(values["rows"], &rows) != nil || cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
		return TerminalEvent{}, ErrInvalidTerminalGeometry
	}
	return TerminalEvent{Cols: cols, Rows: rows}, nil
}

func (t *Terminal) Input(ctx context.Context, data []byte) error {
	if len(data) > 1<<20 {
		return ErrTerminalInputTooLarge
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case t.writeGate <- struct{}{}:
	}
	defer func() { <-t.writeGate }()
	for len(data) > 0 {
		n := min(len(data), terminalMessageLimit)
		if err := t.conn.Write(ctx, websocket.MessageBinary, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (t *Terminal) Resize(ctx context.Context, cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
		return ErrInvalidTerminalGeometry
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case t.writeGate <- struct{}{}:
	}
	defer func() { <-t.writeGate }()
	data, _ := json.Marshal(struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{"resize", cols, rows})
	return t.conn.Write(ctx, websocket.MessageText, data)
}

func (t *Terminal) Close() error { return t.conn.CloseNow() }
