package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/coder/websocket"
)

func TestEventsCursorAndLifecycle(t *testing.T) {
	for _, cursor := range []*int64{nil, new(int64), func() *int64 { n := int64(999); return &n }()} {
		name := "absent"
		if cursor != nil {
			if *cursor == 0 {
				name = "zero"
			} else {
				name = "future"
			}
		}
		t.Run(name, func(t *testing.T) {
			detached := make(chan error, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v0/events" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("wrong attachment")
				}
				values, exists := r.URL.Query()["cursor"]
				if cursor == nil && exists {
					t.Error("invented cursor")
				}
				if cursor != nil && (!exists || len(values) != 1 || values[0] != map[string]string{"zero": "0", "future": "999"}[name]) {
					t.Error("cursor changed")
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					detached <- err
					return
				}
				defer conn.CloseNow()
				event := domain.Event{Seq: 1000, At: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), Kind: "selected", Payload: map[string]any{"mission_id": "mission-2"}}
				for _, envelope := range []EventEnvelope{{Type: "snapshot", Events: []domain.Event{event}}, {Type: "event", Event: &event}} {
					data, err := json.Marshal(envelope)
					if err != nil {
						detached <- err
						return
					}
					if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
						detached <- err
						return
					}
				}
				_, _, err = conn.Read(r.Context())
				detached <- err
			}))
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := New(s.URL, "secret").DialEvents(ctx, cursor)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for _, kind := range []string{"snapshot", "event"} {
				env, err := stream.Read(context.Background())
				if err != nil || env.Type != kind {
					t.Fatalf("%+v %v", env, err)
				}
				event := env.Event
				if kind == "snapshot" {
					if len(env.Events) != 1 {
						t.Fatal(env)
					}
					event = &env.Events[0]
				}
				if event.Seq != 1000 || event.Kind != "selected" || event.At.Year() != 2026 || event.Payload["mission_id"] != "mission-2" {
					t.Fatalf("event %+v", event)
				}
			}
			cancel()
			if err := <-detached; err == nil {
				t.Fatal("idle cancellation did not detach")
			}
			_, err = stream.Read(context.Background())
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel cause %v", err)
			}
		})
	}
}

func TestEventsMalformedAndDisconnect(t *testing.T) {
	for _, tt := range []struct {
		name, body         string
		binary, disconnect bool
	}{
		{name: "snapshot bound", body: `{"type":"snapshot","events":[` + strings.Repeat(`{},`, 200) + `{}]}`},
		{name: "invalid JSON", body: "{"}, {name: "wrong type", body: `{"type":"other"}`}, {name: "mixed", body: `{"type":"snapshot","event":{"seq":1}}`}, {name: "binary", body: "{}", binary: true}, {name: "disconnect", disconnect: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				if tt.disconnect {
					return
				}
				kind := websocket.MessageText
				if tt.binary {
					kind = websocket.MessageBinary
				}
				if err := conn.Write(r.Context(), kind, []byte(tt.body)); err != nil {
					t.Error(err)
				}
				if _, _, err := conn.Read(r.Context()); err == nil {
					t.Error("unexpected client frame")
				}
			}))
			defer s.Close()
			stream, err := New(s.URL, "secret").DialEvents(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_, err = stream.Read(context.Background())
			requireOperationError(t, err, 0, EffectReadOnly)
		})
	}
}

func TestEventsReadCancellationAndExplicitClose(t *testing.T) {
	for _, mode := range []string{"read cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			detached := make(chan error, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					detached <- err
					return
				}
				defer conn.CloseNow()
				_, _, err = conn.Read(r.Context())
				detached <- err
			}))
			defer s.Close()
			stream, err := New(s.URL, "secret").DialEvents(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if mode == "close" {
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				_, err = stream.Read(context.Background())
				if err == nil {
					t.Fatal("closed read succeeded")
				}
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err = stream.Read(ctx)
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			if err := <-detached; err == nil {
				t.Fatal("connection stayed attached")
			}
		})
	}
}

func TestMutationRedirectNeverReplays(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				t.Errorf("redirect received token %q", r.Header.Get("Authorization"))
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing bearer")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				if _, err := io.WriteString(w, `{"error":"redirect","message":"not followed"}`); err != nil {
					t.Error(err)
				}
			}))
			defer source.Close()
			_, err := New(source.URL, "secret").CreateAccount(context.Background(), CreateAccountRequest{Label: "selected"})
			requireOperationError(t, err, status, EffectUncertain)
			if received.Load() != 0 {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestEventRedirectNeverReceivesAuthentication(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				t.Error("event redirect recipient received a request")
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v0/events" || r.URL.Query().Get("cursor") != "0" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("event attachment identity changed")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				if _, err := io.WriteString(w, `{"error":"redirect","message":"not followed"}`); err != nil {
					t.Error(err)
				}
			}))
			defer source.Close()
			cursor := int64(0)
			stream, err := New(source.URL, "secret").DialEvents(context.Background(), &cursor)
			requireOperationError(t, err, status, EffectReadOnly)
			var failure *OperationError
			if stream != nil || !errors.As(err, &failure) || failure.Code != "redirect" || failure.Message != "not followed" || received.Load() != 0 {
				t.Fatalf("redirect attachment %+v error %+v received %d", stream, failure, received.Load())
			}
		})
	}
}

type cancelAfterResponseRead struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelAfterResponseRead) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.cancel()
	}
	return n, err
}

func TestMutationCanceledDuringResponse(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(release)
		if r.Method != http.MethodPost || r.URL.Path != "/v0/accounts" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong mutation request")
		}
		w.WriteHeader(200)
		if _, err := io.WriteString(w, `{"id":`); err != nil {
			t.Error(err)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(s.URL, "secret")
	transport := s.Client().Transport
	c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
		res, err := transport.RoundTrip(r)
		if err != nil {
			return res, err
		}
		// Reading actual response bytes proves the client has received the status.
		res.Body = &cancelAfterResponseRead{ReadCloser: res.Body, cancel: cancel}
		return res, nil
	})
	_, err := c.CreateAccount(ctx, CreateAccountRequest{})
	requireOperationError(t, err, 200, EffectUncertain)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-release
}

func TestTerminalExactAttachment(t *testing.T) {
	detached := make(chan error, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/sessions/session-2/terminal" || r.URL.Query().Get("client") != "client & 2" || r.URL.Query().Get("token") != "secret" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("terminal identity changed")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			detached <- err
			return
		}
		defer conn.CloseNow()
		_, _, err = conn.Read(r.Context())
		detached <- err
	}))
	defer s.Close()
	conn, err := New(s.URL, "secret").DialTerminal(context.Background(), "session-2", "client & 2")
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseNow(); err != nil {
		t.Fatal(err)
	}
	if err := <-detached; err == nil {
		t.Fatal("expected detach")
	}
}
