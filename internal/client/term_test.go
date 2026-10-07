package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestTerminalV1RejectsUnnegotiatedServer(t *testing.T) {
	var attempts atomic.Int32
	joined := make(chan struct{})
	ts, httpClient := terminalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		defer close(joined)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_ = c.Write(r.Context(), websocket.MessageBinary, []byte("not qualified"))
		_, _, _ = c.Read(r.Context())
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	api := New(ts.URL, "token")
	api.HTTP = httpClient
	c, err := api.DialTerminalV1(ctx, "exact-session", "exact-client")
	if c != nil || !errors.Is(err, ErrUnsupportedTerminalProtocol) {
		t.Fatalf("old server exposed: %v %v", c, err)
	}
	select {
	case <-joined:
	case <-ctx.Done():
		t.Fatal("old server socket did not close")
	}
	if attempts.Load() != 1 {
		t.Fatalf("unexpected automatic retry: %d", attempts.Load())
	}
}

func terminalTestServer(t *testing.T, h http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, ts.Client()
}

func TestTerminalV1TypedWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	serverResult := make(chan error, 1)
	ts, httpClient := terminalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/sessions/exact-session/terminal" || r.URL.Query().Get("client") != "exact-client" || r.URL.Query().Get("token") != "token" || r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("Sec-WebSocket-Protocol") != TerminalProtocol {
			serverResult <- fmt.Errorf("incorrect identity or negotiation: %s %v", r.URL, r.Header)
			w.WriteHeader(400)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{TerminalProtocol}})
		if err != nil {
			serverResult <- err
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(64 << 10)
		for _, msg := range []struct {
			typ  websocket.MessageType
			data []byte
		}{
			{websocket.MessageText, []byte(`{"type":"geometry","cols":80,"rows":24}`)},
			{websocket.MessageBinary, payload[:64<<10]},
			{websocket.MessageBinary, []byte("LIVE")},
		} {
			if err = c.Write(ctx, msg.typ, msg.data); err != nil {
				serverResult <- err
				return
			}
		}
		var received []byte
		for len(received) < len(payload) {
			typ, b, err := c.Read(ctx)
			if err != nil || typ != websocket.MessageBinary || len(b) > 64<<10 {
				serverResult <- fmt.Errorf("input frame %d/%d: %v", typ, len(b), err)
				return
			}
			received = append(received, b...)
		}
		if !bytes.Equal(received, payload) {
			serverResult <- fmt.Errorf("input order mismatch")
			return
		}
		typ, b, err := c.Read(ctx)
		if err != nil || typ != websocket.MessageText || string(b) != `{"type":"resize","cols":97,"rows":31}` {
			serverResult <- fmt.Errorf("resize/no oversized paste: %d %q %v", typ, b, err)
			return
		}
		if err = c.Write(ctx, websocket.MessageText, []byte(`{"type":"geometry","cols":97,"rows":31}`)); err != nil {
			serverResult <- err
			return
		}
		serverResult <- c.Close(websocket.StatusNormalClosure, "")
	}))
	api := New(ts.URL, "token")
	api.HTTP = httpClient
	c, err := api.DialTerminalV1(ctx, "exact-session", "exact-client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	e, err := c.Read(ctx)
	if err != nil || e.Cols != 80 || e.Rows != 24 || e.Data != nil {
		t.Fatalf("initial %+v %v", e, err)
	}
	e, err = c.Read(ctx)
	if err != nil || !bytes.Equal(e.Data, payload[:64<<10]) {
		t.Fatalf("64KiB output: %d %v", len(e.Data), err)
	}
	e, err = c.Read(ctx)
	if err != nil || string(e.Data) != "LIVE" {
		t.Fatalf("live %+v %v", e, err)
	}
	if err = c.Input(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if err = c.Input(ctx, make([]byte, (1<<20)+1)); !errors.Is(err, ErrTerminalInputTooLarge) {
		t.Fatalf("oversized paste: %v", err)
	}
	if err = c.Resize(ctx, 97, 31); err != nil {
		t.Fatal(err)
	}
	e, err = c.Read(ctx)
	if err != nil || e.Cols != 97 || e.Rows != 31 || e.Data != nil {
		t.Fatalf("resize %+v %v", e, err)
	}
	_, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("ordinary close: %v", err)
	}
	select {
	case err := <-serverResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not join")
	}
}

func TestTerminalV1RejectsMetadataAndOversizedOutput(t *testing.T) {
	cases := []struct {
		name, data string
		typ        websocket.MessageType
		initial    bool
		code       websocket.StatusCode
	}{
		{"binary-before-geometry", "unqualified", websocket.MessageBinary, false, 1008},
		{"invalid-json", "{", websocket.MessageText, false, 1008},
		{"unknown-type", `{"type":"title","cols":80,"rows":24}`, websocket.MessageText, true, 1008},
		{"extra-field", `{"type":"geometry","cols":80,"rows":24,"x":1}`, websocket.MessageText, true, 1008},
		{"duplicate-field", `{"type":"geometry","cols":80,"cols":81,"rows":24}`, websocket.MessageText, true, 1008},
		{"missing-field", `{"type":"geometry","cols":80}`, websocket.MessageText, true, 1008},
		{"fraction", `{"type":"geometry","cols":80.5,"rows":24}`, websocket.MessageText, true, 1008},
		{"zero", `{"type":"geometry","cols":80,"rows":0}`, websocket.MessageText, true, 1008},
		{"range", `{"type":"geometry","cols":65536,"rows":24}`, websocket.MessageText, true, 1008},
		{"trailing-json", `{"type":"geometry","cols":80,"rows":24}{}`, websocket.MessageText, true, 1008},
		{"oversized-binary", strings.Repeat("x", 65537), websocket.MessageBinary, true, 1009},
		{"oversized-text", strings.Repeat("x", 65537), websocket.MessageText, true, 1009},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			closed := make(chan error, 1)
			ts, httpClient := terminalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{TerminalProtocol}})
				if err != nil {
					closed <- err
					return
				}
				defer c.CloseNow()
				if tc.initial {
					if err = c.Write(ctx, websocket.MessageText, []byte(`{"type":"geometry","cols":80,"rows":24}`)); err != nil {
						closed <- err
						return
					}
				}
				_ = c.Write(ctx, tc.typ, []byte(tc.data))
				_, _, err = c.Read(ctx)
				closed <- err
			}))
			api := New(ts.URL, "token")
			api.HTTP = httpClient
			c, err := api.DialTerminalV1(ctx, "s", "c")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if tc.initial {
				if _, err = c.Read(ctx); err != nil {
					t.Fatal(err)
				}
			}
			e, err := c.Read(ctx)
			if err == nil || e.Data != nil || e.Cols != 0 || e.Rows != 0 {
				t.Fatalf("exposed invalid event %+v %v", e, err)
			}
			if tc.code == 1008 && !errors.Is(err, ErrInvalidTerminalGeometry) {
				t.Fatalf("classification: %v", err)
			}
			select {
			case err := <-closed:
				var closeErr websocket.CloseError
				if !errors.As(err, &closeErr) || closeErr.Code != tc.code {
					t.Fatalf("peer close: %v", err)
				}
				if tc.code == 1008 && closeErr.Reason != "invalid_terminal_geometry" {
					t.Fatalf("reason %q", closeErr.Reason)
				}
			case <-ctx.Done():
				t.Fatal("server reader did not join")
			}
		})
	}
}

func TestTerminalLegacyDialWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte("r"), 40<<10)
	done := make(chan error, 1)
	ts, httpClient := terminalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-WebSocket-Protocol") != "" {
			done <- fmt.Errorf("legacy offered protocol")
			w.WriteHeader(400)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer c.CloseNow()
		if err = c.Write(ctx, websocket.MessageBinary, payload); err != nil {
			done <- err
			return
		}
		typ, b, err := c.Read(ctx)
		if err != nil || typ != websocket.MessageText || string(b) != "legacy text" {
			done <- fmt.Errorf("legacy input %d %q %v", typ, b, err)
			return
		}
		done <- c.Close(1000, "")
	}))
	api := New(ts.URL, "token")
	api.HTTP = httpClient
	c, err := api.DialTerminal(ctx, "s", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if c.Subprotocol() != "" {
		t.Fatalf("legacy negotiated %q", c.Subprotocol())
	}
	typ, b, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || !bytes.Equal(b, payload) {
		t.Fatalf("legacy output %d/%d %v", typ, len(b), err)
	}
	if err = c.Write(ctx, websocket.MessageText, []byte("legacy text")); err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != 1000 {
		t.Fatalf("legacy close %v", err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("legacy handler did not join")
	}
}
