package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNeverShipControlTraceDisabledIdentity(t *testing.T) {
	h := http.NewServeMux()
	rt := &http.Transport{}
	dial := (&terminalPipeListener{}).dial
	d := newNeverShipControlTrace(false, "claude", io.Discard)
	if d != nil || d.handler(h) != h || d.transport(rt) != rt || reflect.ValueOf(d.dial(dial)).Pointer() != reflect.ValueOf(dial).Pointer() {
		t.Fatal("disabled observer must return original objects")
	}
	if n := testing.AllocsPerRun(100, func() {
		x := newNeverShipControlTrace(false, "claude", io.Discard)
		_ = x.handler(h)
		_ = x.transport(rt)
		_ = x.dial(dial)
	}); n != 0 {
		t.Fatalf("disabled allocations=%v", n)
	}
}

// Real authenticated API / Manager / SQLite / PTY over net.Pipe. The gate holds
// only the response after the actual control handler has taken and persisted.
func neverShipControlFixture(t *testing.T, d *neverShipControlTrace, gate *neverShipControlGate) (*client.Client, *session.Manager, *store.Store) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager := session.New(root)
	pending, err := manager.StartPending(domain.Session{ID: "actual-login", Work: "login", Adapter: "claude", Cols: 80, Rows: 24, Argv: testfixture.Argv(t, []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec /bin/cat"})}, testfixture.TerminalEnvironment(t, []string{"HOME=" + root}), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Persist(st.PutSession); err != nil {
		t.Fatal(err)
	}
	listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
	handler := d.handler((&api.Server{Svc: service.New(st, manager, nil), Token: "disposable-control-fixture"}).Handler())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate != nil && r.Method == http.MethodPost && r.URL.Path == "/v0/sessions/actual-login/control" {
			defer close(gate.returned)
			handler.ServeHTTP(&neverShipControlHeldWriter{ResponseWriter: w, gate: gate}, r)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(listener) }()
	rt := &http.Transport{DialContext: d.dial(listener.dial)}
	c := client.New("http://account-fixture", "disposable-control-fixture")
	var delegate http.RoundTripper = rt
	if gate != nil && gate.flush {
		delegate = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
			response, err := rt.RoundTrip(r)
			if err == nil {
				response.Body = &neverShipControlReadSignal{ReadCloser: response.Body, gate: gate}
			}
			return response, err
		})
	}
	c.HTTP = &http.Client{Transport: d.transport(delegate), Timeout: 5 * time.Second}
	t.Cleanup(func() {
		if gate != nil {
			gate.releaseOnce.Do(func() { close(gate.release) })
			<-gate.returned
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		_ = server.Close()
		_ = listener.Close()
		rt.CloseIdleConnections()
		<-served
		d.cleanup()
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, manager, st
}

type neverShipControlGate struct {
	entered, release, returned chan struct{}
	releaseOnce                sync.Once
	readOnce                   sync.Once
	readEntered                chan struct{}
	flush                      bool
}
type neverShipControlHeldWriter struct {
	http.ResponseWriter
	gate *neverShipControlGate
}

func (w *neverShipControlHeldWriter) Write(b []byte) (int, error) {
	if w.gate.flush {
		if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
			return 0, err
		}
	}
	close(w.gate.entered)
	<-w.gate.release
	return w.ResponseWriter.Write(b)
}
func neverShipNewGate() *neverShipControlGate {
	return &neverShipControlGate{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}), readEntered: make(chan struct{})}
}

func TestNeverShipControlTraceActualSuccess(t *testing.T) {
	var out bytes.Buffer
	d := newNeverShipControlTrace(true, "claude", &out)
	c, manager, st := neverShipControlFixture(t, d, nil)
	if err := manager.Input("actual-login", "new-client", []byte("DENIED")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("pre-ack input guard: %v", err)
	}
	reply, err := c.ControlSession(context.Background(), "actual-login", client.ControlSessionRequest{Surface: "tui", Client: "new-client"})
	if err != nil {
		t.Fatal(err)
	}
	live, _ := manager.Snapshot("actual-login")
	stored, err := st.GetSession("actual-login")
	if err != nil || !reflect.DeepEqual(reply, live) || stored.OwnerClient != reply.OwnerClient || reply.OwnerClient != "new-client" {
		t.Fatal("real control reply/persistence changed")
	}
	if err := manager.Input("actual-login", "old-client", []byte("DENIED")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("foreign input guard: %v", err)
	}
	d.cleanup()
	for _, want := range []string{"client_entry", "get_conn", "dial_enter", "dial_return", "got_conn", "request_written", "handler_enter", "handler_return", "response_write_enter", "response_write_return", "first_response_byte", "transport_return", "body_read_return", "body_close_return", "watcher_joined=true", "decode=unobservable", "control_return=unobservable"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
	neverShipControlLogGuard(t, out.String())
	if strings.Contains(out.String(), "ctx_done") {
		t.Fatal("successful control reported expiry")
	}
}

// Manually signal the caller context's deadline error after the real handler
// reaches its gate. No elapsed-time assertion or sleep controls the experiment.
type neverShipExpiryContext struct {
	context.Context
	done chan struct{}
}

func (c *neverShipExpiryContext) Done() <-chan struct{} { return c.done }
func (c *neverShipExpiryContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func TestNeverShipControlTraceActualExpiry(t *testing.T) {
	for _, mode := range []string{"response_held", "body_held", "capture_failure"} {
		t.Run(mode, func(t *testing.T) {
			brokenSink := mode == "capture_failure"
			sink := &neverShipControlSink{fail: brokenSink, expiry: make(chan struct{})}
			d := newNeverShipControlTrace(true, "claude", sink)
			gate := neverShipNewGate()
			gate.flush = mode == "body_held"
			c, manager, st := neverShipControlFixture(t, d, gate)
			ctx := &neverShipExpiryContext{Context: context.Background(), done: make(chan struct{})}
			result := make(chan error, 1)
			go func() {
				_, err := c.ControlSession(ctx, "actual-login", client.ControlSessionRequest{Surface: "tui", Client: "new-client"})
				result <- err
			}()
			<-gate.entered
			if gate.flush {
				<-gate.readEntered
			}
			live, _ := manager.Snapshot("actual-login")
			stored, err := st.GetSession("actual-login")
			if err != nil || live.OwnerClient != "new-client" || stored.OwnerClient != "new-client" {
				t.Fatal("gate did not follow real Take and Store")
			}
			close(ctx.done)
			err = <-result
			var operation *client.OperationError
			wantStatus := 0
			if gate.flush {
				wantStatus = http.StatusOK
			}
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &operation) || operation.Effect != client.EffectUncertain || operation.Status != wantStatus {
				t.Fatalf("original deadline/error effect lost: %v", err)
			}
			// Observe evidence before releasing the API handler, without a timer.
			if !brokenSink {
				select {
				case <-sink.expiry:
				default:
					t.Fatal("no prompt evidence before client return")
				}
				for _, want := range []string{"ctx_done", "error=deadline", "handler_active=true", "response_write_enter", "stack_handler=found", "(*Server).control", "phase=response_write_enter"} {
					if !strings.Contains(sink.String(), want) {
						t.Errorf("expiry missing %s", want)
					}
				}
			}
			if gate.flush {
				if !strings.Contains(sink.String(), "stage=body_read_enter") {
					t.Error("missing body read entry")
				}
			}
			gate.releaseOnce.Do(func() { close(gate.release) })
			<-gate.returned
			d.cleanup()
			if !brokenSink && (!strings.Contains(sink.String(), "handler_return") || !strings.Contains(sink.String(), "watcher_joined=true")) {
				t.Fatal("handler lifetime or watcher join absent")
			}
			neverShipControlLogGuard(t, sink.String())
			if len(sink.String()) > 32<<10 {
				t.Fatal("unbounded output")
			}
		})
	}
}

type neverShipControlSink struct {
	mu     sync.Mutex
	data   bytes.Buffer
	fail   bool
	expiry chan struct{}
	once   sync.Once
}

func (s *neverShipControlSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, errors.New("synthetic capture failure")
	}
	n, err := s.data.Write(p)
	if bytes.Contains(p, []byte("ctx_done")) {
		s.once.Do(func() { close(s.expiry) })
	}
	return n, err
}
func (s *neverShipControlSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.String()
}

// Signal from inside the observer's real body read, after its entry milestone.
type neverShipControlReadSignal struct {
	io.ReadCloser
	gate *neverShipControlGate
}

func (b *neverShipControlReadSignal) Read(p []byte) (int, error) {
	b.gate.readOnce.Do(func() { close(b.gate.readEntered) })
	return b.ReadCloser.Read(p)
}
func neverShipControlLogGuard(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{"disposable-control-fixture", "new-client", "old-client", "BODY_CANARY", "Authorization", "Bearer", "Cookie", "auth.secret", "HOME=", "catalog.db", "Argv", "/Volumes/", `C:\Users\`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostic exposed forbidden field %q", forbidden)
		}
	}
}

func TestNeverShipControlTraceBoundsAndCaptureFailure(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounds", true: "failed_sink_success_preserved"}[broken], func(t *testing.T) {
			sink := &neverShipControlSink{fail: broken, expiry: make(chan struct{})}
			d := newNeverShipControlTrace(true, "BODY_CANARY", sink)
			c, manager, _ := neverShipControlFixture(t, d, nil)
			// One-byte reads force more than 48 real body milestones. Six distinct
			// requests exceed the four-trace cap; every actual API reply must survive.
			for i := 0; i < 6; i++ {
				req, err := http.NewRequest(http.MethodPost, c.Base+"/v0/sessions/actual-login/control", strings.NewReader(`{"surface":"tui","client":"BODY_CANARY"}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+c.Token)
				req.Header.Set("Cookie", "BODY_CANARY")
				response, err := c.HTTP.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				var body bytes.Buffer
				var one [1]byte
				for {
					n, err := response.Body.Read(one[:])
					body.Write(one[:n])
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
				var reply domain.Session
				if err := json.Unmarshal(body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				live, _ := manager.Snapshot("actual-login")
				if !reflect.DeepEqual(reply, live) || reply.OwnerClient != "BODY_CANARY" {
					t.Fatal("capture altered actual reply")
				}
			}
			d.cleanup()
			text := sink.String()
			neverShipControlLogGuard(t, text)
			if !broken {
				if !strings.Contains(text, "requests_dropped=2") || !strings.Contains(text, "records=48 records_dropped=") || strings.Contains(text, "records=48 records_dropped=0") {
					t.Fatal("missing bounded-drop evidence")
				}
				if strings.Count(text, "snapshot=cleanup") != 4 || len(text) > 4*(12<<10)+512 {
					t.Fatal("trace/output cap violated")
				}
			}
		})
	}
}
