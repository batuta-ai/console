//go:build darwin || linux

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/coder/websocket"
)

func TestTerminalV1WireAndLegacyBinary(t *testing.T) {
	h, svc := testServer(t)
	_, err := svc.Session.Start(domain.Session{ID: "wire", Cols: 80, Rows: 24, Argv: []string{"/bin/sh", "-c", `stty raw -echo; printf READY; cat`}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Session.Stop("wire")
	if _, err = svc.Session.Take("wire", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	ts, httpClient := terminalTestServer(t, h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v0/sessions/wire/terminal?client=owner&token=test-token"
	for _, proto := range []string{"console-terminal-v1", ""} {
		t.Run(proto, func(t *testing.T) {
			opt := &websocket.DialOptions{HTTPClient: httpClient}
			if proto != "" {
				opt.Subprotocols = []string{proto}
			}
			c, _, err := websocket.Dial(ctx, url, opt)
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			if c.Subprotocol() != proto {
				t.Fatalf("negotiated %q", c.Subprotocol())
			}
			c.SetReadLimit(64 << 10)
			if proto != "" {
				typ, b, err := c.Read(ctx)
				if err != nil || typ != websocket.MessageText || string(b) != `{"type":"geometry","cols":80,"rows":24}` {
					t.Fatalf("initial geometry: %d %s %v", typ, b, err)
				}
			}
			var ready []byte
			for !bytes.Contains(ready, []byte("READY")) {
				typ, b, err := c.Read(ctx)
				if err != nil || typ != websocket.MessageBinary {
					t.Fatalf("ready %d %s %v", typ, b, err)
				}
				ready = append(ready, b...)
			}
			literal := []byte(`{"type":"resize","cols":97,"rows":31}`)
			if err = c.Write(ctx, websocket.MessageBinary, literal); err != nil {
				t.Fatal(err)
			}
			var got []byte
			for len(got) < len(literal) {
				typ, b, err := c.Read(ctx)
				if err != nil || typ != websocket.MessageBinary {
					t.Fatalf("literal %d %s %v", typ, b, err)
				}
				got = append(got, b...)
			}
			if !bytes.Equal(got, literal) {
				t.Fatalf("literal paste %q", got)
			}
			snap, _ := svc.Session.Snapshot("wire")
			if snap.Cols != 80 || snap.Rows != 24 {
				t.Fatalf("binary paste resized: %+v", snap)
			}
		})
	}
}

func TestTerminalProtocolAuthenticationOrder(t *testing.T) {
	h, _ := testServer(t)
	for _, token := range []string{"", "test-token"} {
		r := httptest.NewRequest(http.MethodGet, "/v0/sessions/missing/terminal?token="+token, nil)
		r.Header.Set("Sec-WebSocket-Protocol", "unknown")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := http.StatusBadRequest
		if token == "" {
			want = http.StatusUnauthorized
		}
		if w.Code != want {
			t.Fatalf("token %q: %d %s", token, w.Code, w.Body.String())
		}
		if token != "" && !strings.Contains(w.Body.String(), "unsupported_terminal_protocol") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestTerminalV1InvalidControlsAndOversize(t *testing.T) {
	h, svc := testServer(t)
	_, err := svc.Session.Start(domain.Session{ID: "controls", Argv: []string{"/bin/cat"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Session.Stop("controls")
	_, err = svc.Session.Take("controls", "tui", "owner")
	if err != nil {
		t.Fatal(err)
	}
	ts, httpClient := terminalTestServer(t, h)
	defer ts.Close()
	for _, tc := range []struct {
		data   string
		code   websocket.StatusCode
		reason string
	}{
		{`broken`, 1008, "invalid_terminal_control"},
		{`{"type":"input"}`, 1008, "invalid_terminal_control"},
		{`{"type":"resize","cols":80,"rows":24,"extra":1}`, 1008, "invalid_terminal_control"},
		{`{"type":"resize","cols":80,"cols":81,"rows":24}`, 1008, "invalid_terminal_control"},
		{`{"type":"resize","cols":80,"rows":24}{}`, 1008, "invalid_terminal_control"},
		{`{"type":"resize","cols":80}`, 1008, "invalid_terminal_resize"},
		{`{"type":"resize","cols":80,"rows":null}`, 1008, "invalid_terminal_resize"},
		{`{"type":"resize","cols":80,"rows":-1}`, 1008, "invalid_terminal_resize"},
		{`{"type":"resize","cols":1.5,"rows":24}`, 1008, "invalid_terminal_resize"},
		{`{"type":"resize","cols":0,"rows":24}`, 1008, "invalid_terminal_resize"},
		{`{"type":"resize","cols":65536,"rows":24}`, 1008, "invalid_terminal_resize"},
		{strings.Repeat("x", 65537), 1009, ""},
	} {
		t.Run(tc.reason+tc.data[:min(15, len(tc.data))], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/sessions/controls/terminal?client=owner&token=test-token", &websocket.DialOptions{HTTPClient: httpClient, Subprotocols: []string{"console-terminal-v1"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			if _, _, err = c.Read(ctx); err != nil {
				t.Fatal(err)
			}
			_ = c.Write(ctx, websocket.MessageText, []byte(tc.data))
			_, _, err = c.Read(ctx)
			if websocket.CloseStatus(err) != tc.code {
				t.Fatalf("close: %v", err)
			}
			if tc.reason != "" && !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("reason: %v", err)
			}
		})
	}
	snap, _ := svc.Session.Snapshot("controls")
	b, _ := json.Marshal(snap)
	if snap.Process != "running" || snap.OwnerClient != "owner" {
		t.Fatal(string(b))
	}
}

func terminalTestServer(t *testing.T, h http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, ts.Client()
}
