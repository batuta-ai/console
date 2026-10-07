package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type contractTransport func(*http.Request) (*http.Response, error)

func (f contractTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func contractResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func requireOperationError(t *testing.T, err error, status int, effect OperationEffect) *OperationError {
	t.Helper()
	var e *OperationError
	if !errors.As(err, &e) || e.Status != status || e.Effect != effect {
		t.Fatalf("error = %#v, want status %d effect %s", err, status, effect)
	}
	return e
}
func TestPureEventHandshakeError(t *testing.T) {
	c := New("http://fixture", "secret")
	c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v0/events" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request %v", r)
		}
		return contractResponse(403, `{"error":"not_owner","message":"selected client"}`), nil
	})
	_, err := c.DialEvents(context.Background(), nil)
	e := requireOperationError(t, err, 403, EffectReadOnly)
	if e.Code != "not_owner" || e.Message != "selected client" || e.Cause == nil {
		t.Fatalf("handshake error %+v", e)
	}
}

type failedContractBody struct{ err error }

func (b failedContractBody) Read([]byte) (int, error) { return 0, b.err }
func (b failedContractBody) Close() error             { return nil }
func TestPureMutationFailures(t *testing.T) {
	lost := errors.New("lost response")
	tests := []struct {
		name      string
		status    int
		body      string
		cause     error
		transport bool
		code      string
		effect    OperationEffect
	}{
		{name: "lost", cause: lost, transport: true, effect: EffectUncertain},
		{name: "read", status: 200, cause: io.ErrUnexpectedEOF, effect: EffectUncertain},
		{name: "canceled after dispatch", cause: context.Canceled, transport: true, effect: EffectUncertain},
		{name: "server", status: 500, body: `{"error":"store","message":"possibly stored"}`, code: "store", effect: EffectUncertain},
		{name: "not owner", status: 403, body: `{"error":"not_owner","message":"owner"}`, code: "not_owner", effect: EffectUncertain},
		{name: "invalid control", status: 400, body: `{"error":"invalid_control","message":"surface"}`, code: "invalid_control", effect: EffectUncertain},
		{name: "auth", status: 401, body: `{"error":"unauthorized","message":"token"}`, code: "unauthorized", effect: EffectRejected},
		{name: "malformed", status: 200, body: `{`, effect: EffectUncertain},
		{name: "oversize", status: 200, body: strings.Repeat("x", maxOperationBytes+1), effect: EffectUncertain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			c := New("http://fixture", "secret")
			c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.GetBody != nil {
					t.Fatal("replayable mutation")
				}
				if tt.transport {
					return nil, tt.cause
				}
				res := contractResponse(tt.status, tt.body)
				if tt.cause != nil {
					res.Body = failedContractBody{tt.cause}
				}
				return res, nil
			})
			_, err := c.ControlSession(context.Background(), "selected", ControlSessionRequest{Surface: "none", Client: "client"})
			e := requireOperationError(t, err, tt.status, tt.effect)
			if calls != 1 || e.Code != tt.code {
				t.Fatalf("calls %d error %+v", calls, e)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Fatalf("lost cause %v", err)
			}
		})
	}
}
func TestPureRequestBoundAndReadCancellation(t *testing.T) {
	c := New("http://fixture", "secret")
	calls := 0
	c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, err := c.CreateAccount(context.Background(), CreateAccountRequest{Label: strings.Repeat("x", maxOperationBytes)})
	requireOperationError(t, err, 0, EffectNotDispatched)
	if calls != 0 {
		t.Fatal("oversized request dispatched")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.ListAccounts(ctx)
	requireOperationError(t, err, 0, EffectReadOnly)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(err)
	}
}

func TestPureEventHandshakeLongMessage(t *testing.T) {
	c := New("http://fixture", "secret")
	message := strings.Repeat("m", 2048)
	c.HTTP.Transport = contractTransport(func(*http.Request) (*http.Response, error) {
		return contractResponse(403, `{"error":"not_owner","message":"`+message+`"}`), nil
	})
	_, err := c.DialEvents(context.Background(), nil)
	e := requireOperationError(t, err, 403, EffectReadOnly)
	if e.Code != "not_owner" || e.Message != message {
		t.Fatalf("lost handshake message: %+v", e)
	}
}

func TestPureEventHandshakeBodyFailures(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		cause      error
	}{
		{name: "partial", cause: io.ErrUnexpectedEOF},
		{name: "canceled read", cause: context.Canceled},
		{name: "malformed", body: "{"},
		{name: "oversize", body: strings.Repeat("x", maxOperationBytes+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := New("http://fixture", "secret")
			c.HTTP.Transport = contractTransport(func(*http.Request) (*http.Response, error) {
				res := contractResponse(500, tt.body)
				if tt.cause != nil {
					res.Body = failedContractBody{tt.cause}
				}
				return res, nil
			})
			_, err := c.DialEvents(context.Background(), nil)
			e := requireOperationError(t, err, 500, EffectReadOnly)
			if e.Cause == nil || tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Fatalf("lost cause %v", err)
			}
		})
	}
}
