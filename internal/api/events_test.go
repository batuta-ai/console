package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/coder/websocket"
)

type eventMessage struct {
	Type   string         `json:"type"`
	Events []domain.Event `json:"events"`
	Event  domain.Event   `json:"event"`
}

func readEventMessage(t *testing.T, c *websocket.Conn) eventMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m eventMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Completion is observed around the real handler, without a production hook.
func eventSocketServer(t *testing.T, h http.Handler, requestContext context.Context) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{}, 16)
	lifetime, stop := context.WithCancel(context.Background())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { done <- struct{}{} }()
		base := r.Context()
		if requestContext != nil {
			base = requestContext
		}
		ctx, cancel := context.WithCancel(base)
		defer cancel()
		stopRequest := context.AfterFunc(lifetime, cancel)
		defer stopRequest()
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { stop(); ts.Close() })
	return ts, done
}

func awaitEventHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event handler remained active after idle disconnect/cancellation")
	}
}

func dialEvents(t *testing.T, ts *httptest.Server, query string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/events?token=test-token"+query, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {ts.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func appendEvents(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := st.AppendEvent("test", map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventCursorSequence(t *testing.T) {
	for _, tc := range []struct {
		name, query  string
		first, count int
	}{
		{"recent", "", 121, 200}, {"zero", "&cursor=0", 1, 200}, {"multiple-pages", "&cursor=40", 41, 200}, {"supplied", "&cursor=237", 238, 83},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, svc := testServer(t)
			appendEvents(t, svc.Store, 320)
			ts, done := eventSocketServer(t, h, nil)
			c := dialEvents(t, ts, tc.query)
			m := readEventMessage(t, c)
			if m.Type != "snapshot" || len(m.Events) != tc.count {
				t.Fatalf("snapshot type=%s count=%d", m.Type, len(m.Events))
			}
			next := int64(tc.first)
			for _, e := range m.Events {
				if e.Seq != next {
					t.Fatalf("snapshot seq=%d want=%d", e.Seq, next)
				}
				next++
			}
			for next <= 320 {
				m = readEventMessage(t, c)
				if m.Type != "event" || m.Event.Seq != next {
					t.Fatalf("catch-up=%+v want=%d", m, next)
				}
				next++
			}
			appendEvents(t, svc.Store, 1)
			m = readEventMessage(t, c)
			if m.Type != "event" || m.Event.Seq != 321 {
				t.Fatalf("live=%+v", m)
			}
			c.CloseNow()
			awaitEventHandler(t, done)
		})
	}
}

func TestEventEmptyAndFutureCursor(t *testing.T) {
	for _, query := range []string{"", "&cursor=0", "&cursor=3"} {
		t.Run(query, func(t *testing.T) {
			h, svc := testServer(t)
			ts, done := eventSocketServer(t, h, nil)
			c := dialEvents(t, ts, query)
			m := readEventMessage(t, c)
			if m.Type != "snapshot" || m.Events == nil || len(m.Events) != 0 {
				t.Fatalf("empty snapshot=%+v", m)
			}
			want := int64(1)
			if query == "&cursor=3" {
				want = 4
			}
			appendEvents(t, svc.Store, int(want))
			m = readEventMessage(t, c)
			if m.Type != "event" || m.Event.Seq != want {
				t.Fatalf("cursor silently reset: %+v want=%d", m, want)
			}
			c.CloseNow()
			awaitEventHandler(t, done)
		})
	}
}

func TestEventCursorRejections(t *testing.T) {
	h, svc := testServer(t)
	appendEvents(t, svc.Store, 3)
	before, err := svc.Store.EventsSince(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	ts, done := eventSocketServer(t, h, nil)
	for _, query := range []string{"&cursor=", "&cursor=-1", "&cursor=wat", "&cursor=9223372036854775808", "&cursor=0&cursor=1", "&cursor=0&cursor=0", "&cursor=%2B1", "&cursor=1.0", "&cursor=%20", "&cursor=%zz"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/events?token=test-token"+query, nil)
		cancel()
		if c != nil {
			c.CloseNow()
		}
		if err == nil || res == nil || res.StatusCode != 400 {
			t.Errorf("query %q upgraded or status !=400: response=%v err=%v", query, res, err)
		} else {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			var diagnostic map[string]string
			if json.Unmarshal(b, &diagnostic) != nil || diagnostic["error"] != "cursor" || len(b) > 512 {
				t.Errorf("unstructured cursor error %q", b)
			}
		}
		awaitEventHandler(t, done)
	}
	after, err := svc.Store.EventsSince(0, 200)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("validation mutated catalog: %v %v", after, err)
	}
	for _, tc := range []struct {
		query, origin string
		status        int
	}{{"", "", 401}, {"?token=wrong", "", 401}, {"?token=test-token", "https://foreign.invalid", 403}} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/events"+tc.query, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {tc.origin}}})
		cancel()
		if c != nil {
			c.CloseNow()
		}
		if err == nil || res == nil || res.StatusCode != tc.status {
			t.Errorf("auth/origin status want=%d response=%v err=%v", tc.status, res, err)
		}
		awaitEventHandler(t, done)
	}
}

func TestEventIdleCleanup(t *testing.T) {
	for _, mode := range []string{"normal", "abrupt", "cancel", "unexpected-message"} {
		t.Run(mode, func(t *testing.T) {
			h, _ := testServer(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ts, done := eventSocketServer(t, h, ctx)
			for i := 0; i < 3; i++ {
				c := dialEvents(t, ts, "")
				readEventMessage(t, c)
				switch mode {
				case "normal":
					if err := c.Close(websocket.StatusNormalClosure, ""); err != nil {
						t.Error(err)
					}
				case "abrupt":
					c.CloseNow()
				case "cancel":
					cancel()
				case "unexpected-message":
					writeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
					if err := c.Write(writeCtx, websocket.MessageText, []byte("unexpected")); err != nil {
						t.Fatal(err)
					}
					_, _, err := c.Read(writeCtx)
					stop()
					if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
						t.Errorf("unexpected message close: %v", err)
					}
				}
				awaitEventHandler(t, done)
				c.CloseNow()
				if mode == "cancel" {
					break
				}
			}
		})
	}
}

func eventFaultCatalog(t *testing.T) (http.Handler, *store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := service.New(st, session.New(t.TempDir()), nil)
	return (&Server{Svc: svc, Token: "test-token"}).Handler(), st, db
}

func TestEventReadFailures(t *testing.T) {
	for _, phase := range []string{"initial", "live"} {
		for _, fault := range []string{"closed", "timestamp", "payload", "encoding"} {
			t.Run(phase+"/"+fault, func(t *testing.T) {
				h, st, db := eventFaultCatalog(t)
				ts, done := eventSocketServer(t, h, nil)
				var c *websocket.Conn
				if phase == "live" {
					c = dialEvents(t, ts, "")
					readEventMessage(t, c)
				}
				if fault == "closed" {
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					at := "broken"
					payload := `{"n":0}`
					if fault == "payload" {
						at = time.Now().UTC().Format(time.RFC3339Nano)
						payload = "{broken"
					}
					if fault == "encoding" {
						at = "2026-01-01T00:00:00+24:00"
					}
					if _, err := db.Exec(`INSERT INTO events(at,kind,payload) VALUES(?,?,?)`, at, "test", payload); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if phase == "initial" {
					conn, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/events?token=test-token", nil)
					if conn != nil {
						conn.CloseNow()
					}
					if err == nil || res == nil || res.StatusCode != 500 {
						t.Errorf("initial failure must reject before upgrade: response=%v err=%v", res, err)
					} else {
						b, _ := io.ReadAll(res.Body)
						res.Body.Close()
						var diagnostic map[string]string
						if json.Unmarshal(b, &diagnostic) != nil || diagnostic["error"] != "events" || strings.Contains(string(b), "sql") || strings.Contains(string(b), "broken") {
							t.Errorf("unsafe/unstructured error %q", b)
						}
					}
				} else {
					_, _, err := c.Read(ctx)
					if websocket.CloseStatus(err) != websocket.StatusInternalError {
						t.Errorf("live catalog failure must close 1011: %v", err)
					}
				}
				awaitEventHandler(t, done)
			})
		}
	}
}

func TestEventPreUpgradeFailures(t *testing.T) {
	for _, query := range []string{"cursor=", "cursor=-1", "cursor=wat", "cursor=9223372036854775808", "cursor=0&cursor=0", "cursor=%2B1", "cursor=%zz"} {
		t.Run(query, func(t *testing.T) {
			h, svc := testServer(t)
			before, err := svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/v0/events?token=test-token&"+query, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			var msg map[string]string
			if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &msg) != nil || msg["error"] != "cursor" {
				t.Errorf("cursor rejected incorrectly: %d %s", rec.Code, rec.Body.String())
			}
			after, err := svc.Store.LastEventSeq()
			if err != nil || after != before {
				t.Fatalf("validation mutated catalog: %d %d %v", before, after, err)
			}
		})
	}
	t.Run("initial-store", func(t *testing.T) {
		h, svc := testServer(t)
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/v0/events?token=test-token", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var msg map[string]string
		if rec.Code != 500 || json.Unmarshal(rec.Body.Bytes(), &msg) != nil || msg["error"] != "events" {
			t.Errorf("initial store failure hidden: %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestEventInitialCorruptionHTTP(t *testing.T) {
	for _, fault := range []string{"timestamp", "payload", "encoding"} {
		t.Run(fault, func(t *testing.T) {
			h, st, db := eventFaultCatalog(t)
			appendEvents(t, st, 1)
			statement := "UPDATE events SET at='broken'"
			if fault == "payload" {
				statement = "UPDATE events SET payload='{broken'"
			}
			if fault == "encoding" {
				statement = "UPDATE events SET at='2026-01-01T00:00:00+24:00'"
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if fault == "encoding" {
				events, err := st.EventsRecent(200)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := json.Marshal(events); err == nil {
					t.Fatal("fixture must fail encoding after successful decoding")
				}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v0/events?token=test-token", nil))
			var msg map[string]string
			if rec.Code != 500 || json.Unmarshal(rec.Body.Bytes(), &msg) != nil || msg["error"] != "events" || strings.Contains(rec.Body.String(), "broken") {
				t.Fatalf("corruption became success or leaked details: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Closing only the real server TCP write half leaves the reader waiting, so
// handler completion must come from the initial/live write failure itself.
func TestEventWriteFailureCleanup(t *testing.T) {
	for _, phase := range []string{"initial", "live"} {
		t.Run(phase, func(t *testing.T) {
			var diagnostics bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			h, svc := testServer(t)
			lifetime, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			hijacked := make(chan *net.TCPConn, 1)
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				h.ServeHTTP(w, r.WithContext(lifetime))
			}))
			ts.Config.ConnState = func(conn net.Conn, state http.ConnState) {
				if state == http.StateHijacked {
					tcp, ok := conn.(*net.TCPConn)
					if !ok {
						t.Errorf("expected real TCP connection")
						return
					}
					if phase == "initial" {
						if err := tcp.CloseWrite(); err != nil {
							t.Errorf("close write: %v", err)
						}
					}
					hijacked <- tcp
				}
			}
			ts.Start()
			t.Cleanup(func() { cancel(); ts.Close() })
			c := dialEvents(t, ts, "")
			var tcp *net.TCPConn
			select {
			case tcp = <-hijacked:
			case <-time.After(2 * time.Second):
				t.Fatal("missing real hijack")
			}
			if phase == "live" {
				readEventMessage(t, c)
				if err := tcp.CloseWrite(); err != nil {
					t.Fatal(err)
				}
				appendEvents(t, svc.Store, 1)
			}
			awaitEventHandler(t, done)
			if !strings.Contains(diagnostics.String(), "event socket write") {
				t.Errorf("write failure had no observable server diagnostic: %s", diagnostics.String())
			}
			c.CloseNow()
		})
	}
}

func TestEventCanceledBeforeUpgrade(t *testing.T) {
	h, _ := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/v0/events?token=test-token", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Body.Len() != 0 {
		t.Fatalf("caller cancellation reported as internal failure: %d %s", rec.Code, rec.Body.String())
	}
}

func TestOrdinaryEventDisconnectEmptyClose(t *testing.T) {
	err := websocket.CloseError{Code: websocket.StatusNoStatusRcvd}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"direct", err}, {"wrapped", fmt.Errorf("read ended: %w", err)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !ordinaryEventDisconnect(context.Background(), tc.err) {
				t.Fatal("empty close must be an ordinary disconnect with a live context")
			}
		})
	}
}

func TestEventIdleEmptyClose(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	h, _ := testServer(t)
	ts, done := eventSocketServer(t, h, nil)
	for i := 0; i < 3; i++ {
		c := dialEvents(t, ts, "")
		readEventMessage(t, c)
		if err := c.Close(websocket.StatusNoStatusRcvd, ""); err != nil {
			t.Fatal(err)
		}
		awaitEventHandler(t, done)
		c.CloseNow()
	}
	if output := logs.String(); strings.Contains(output, "level=WARN") || strings.Contains(output, "test-token") || strings.Contains(output, "1005") {
		t.Fatalf("ordinary empty close diagnostics must be sanitized and contain no warning: %s", output)
	}
}
