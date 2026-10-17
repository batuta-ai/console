package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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

// The barrier observes entry into a real wait, without advancing a clock or
// replacing the Manager, Service callback, persistence, or HTTP server.
type terminalFixtureWaitBarrier struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *terminalFixtureWaitBarrier) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func terminalFixtureTestContext(t *testing.T) context.Context {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("fixture lifecycle tests require go test -timeout")
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	return ctx
}

func terminalFixtureAwait[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("fixture phase did not join: %v", ctx.Err())
	}
	var zero T
	return zero
}

func terminalFixtureExitPersisted(st *store.Store, id string) error {
	row, err := st.GetSession(id)
	if err != nil {
		return err
	}
	if row.Process != "exited" || row.EndedAt == nil {
		return errors.New("exit row not persisted")
	}
	events, err := st.EventsSince(0, 100)
	if err != nil {
		return err
	}
	count := 0
	for _, event := range events {
		if event.Kind == "session.exited" && event.Payload["id"] == id {
			count++
		}
	}
	if count != 1 {
		return errors.New("expected exactly one real session.exited event")
	}
	return nil
}

func TestTerminalFixtureHeldServiceExit(t *testing.T) {
	callbackFailure := errors.New("exit callback qualification failure after real persistence")
	for _, tc := range []struct {
		name             string
		control, expired bool
		callbackErr      error
	}{
		{name: "cancel-while-held"},
		{name: "release-before-cancel", control: true},
		{name: "callback-error", callbackErr: callbackFailure},
		{name: "incomplete-drain-retains-dependencies", expired: true},
	} {
		control := tc.control
		t.Run(tc.name, func(t *testing.T) {
			ctx := terminalFixtureTestContext(t)
			root := terminalFixtureRoot(t)
			st, err := store.Open(filepath.Join(root, "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			manager := session.New(root)
			svc := service.New(st, manager, nil)
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			privateHome, shellHome := "", ""
			original := manager.OnExit
			manager.OnExit = func(s domain.Session) error {
				t.Log("nativeDone (OnExit entry follows owned process/reader join); callback entered")
				close(entered)
				<-release
				t.Log("callback released")
				err := original(s)
				if err == nil {
					err = terminalFixtureExitPersisted(st, s.ID)
				}
				t.Logf("persisted exit: %v; callback returned", err)
				close(returned)
				return errors.Join(err, tc.callbackErr)
			}
			listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
			server := &http.Server{Handler: (&api.Server{Svc: svc, Token: "fixture-lifecycle"}).Handler()}
			served := make(chan error, 1)
			transport := &http.Transport{DialContext: listener.dial}
			f := &terminalFixtureLifecycle{manager: manager, server: server, listener: listener, transport: transport, served: served, root: root}
			server.Handler = f.handlers.wrap(server.Handler)
			go func() { served <- server.Serve(listener) }()
			attempted := make(chan struct{})
			var attemptOnce sync.Once
			f.closeStore = func() error {
				attemptOnce.Do(func() { close(attempted) })
				select {
				case <-returned:
				default:
					return errors.New("StoreClose attempted before real Service callback returned")
				}
				if err := terminalFixtureExitPersisted(st, "held-exit"); err != nil {
					return err
				}
				select {
				case <-f.managerJoin.done:
					t.Log("Manager joined")
				default:
					return errors.New("StoreClose before Manager joined")
				}
				select {
				case <-f.httpJoin.done:
					t.Log("HTTP Serve and handlers/streams joined")
				default:
					return errors.New("StoreClose before HTTP joined")
				}
				if _, err := os.Stat(privateHome); !os.IsNotExist(err) {
					return fmt.Errorf("private HOME not finalized: %v", err)
				}
				transcript, err := os.ReadFile(filepath.Join(root, "held-exit.log"))
				if err != nil || !strings.Contains(string(transcript), "FIXTURE_HOME="+shellHome) {
					return fmt.Errorf("reader transcript missing: %v", err)
				}
				if runtime.GOOS == "windows" && !strings.Contains(string(transcript), "FIXTURE_NATIVE_HOME="+privateHome) {
					return errors.New("reader transcript missing observed native HOME conversion")
				}
				t.Log("private HOME removed; reader transcript retained; StoreClose attempt after persisted exit")
				return st.Close()
			}
			closed := false
			t.Cleanup(func() {
				unblock()
				if !closed {
					// Even a failed assertion must use the same dependency-safe teardown.
					f.closeStore = st.Close
					err := f.finish(ctx, ctx, nil)
					want := ""
					if tc.callbackErr != nil {
						want = "session held-exit exit callback: " + tc.callbackErr.Error()
					}
					if err != nil && err.Error() != want {
						t.Error(err)
					}
				}
				// Expected callback-error cases retain evidence until this assertion ends.
				if _, joined := f.managerJoin.wait(ctx); joined {
					if _, joined := f.httpJoin.wait(ctx); joined {
						if err := os.RemoveAll(root); err != nil {
							t.Error(err)
						}
					}
				}
			})
			script := "printf 'FIXTURE_HOME=%s\n' \"$HOME\"; "
			if runtime.GOOS == "windows" {
				// MSYS HOME is a shell path; native filesystem assertions need its actual conversion.
				script += "fixture_native_home=$(cygpath -w \"$HOME\") || exit; printf 'FIXTURE_NATIVE_HOME=%s\n' \"$fixture_native_home\"; "
			}
			script += "exec /bin/cat"
			pending, err := manager.StartPending(domain.Session{ID: "held-exit", Argv: testfixture.Argv(t, []string{"/bin/sh", "-c", script})}, testfixture.TerminalEnvironment(t, nil), root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pending.Persist(st.PutSession); err != nil {
				t.Fatal(err)
			}
			c := client.New("http://terminal-fixture", "fixture-lifecycle")
			c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second}
			terminal, err := c.DialTerminalV1(ctx, "held-exit", "observer")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = terminal.Close() })
			var output strings.Builder
			for privateHome == "" || shellHome == "" {
				event, err := terminal.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				output.Write(event.Data)
				if _, tail, ok := strings.Cut(output.String(), "FIXTURE_HOME="); ok {
					if home, _, ok := strings.Cut(tail, "\n"); ok {
						shellHome = strings.TrimSuffix(home, "\r")
					}
				}
				if runtime.GOOS == "windows" {
					if _, tail, ok := strings.Cut(output.String(), "FIXTURE_NATIVE_HOME="); ok {
						if home, _, ok := strings.Cut(tail, "\n"); ok {
							privateHome = strings.TrimSuffix(home, "\r")
						}
					}
				} else {
					privateHome = shellHome
				}
			}
			t.Logf("actual private HOME: shell=%q native=%q (Windows conversion: cygpath -w)", shellHome, privateHome)
			if !filepath.IsAbs(privateHome) {
				t.Fatalf("observed private HOME is not a native absolute path: %q", privateHome)
			}
			if _, err := os.Stat(privateHome); err != nil {
				t.Fatal("actual private HOME", err)
			}
			f.startManager()
			terminalFixtureAwait(t, ctx, entered)
			if _, err := os.Stat(privateHome); err != nil {
				t.Error("private HOME removed before callback returned", err)
			}
			initial, cancel := context.WithCancel(context.Background())
			defer cancel()
			if control {
				unblock()
				terminalFixtureAwait(t, ctx, f.managerJoin.done)
				t.Log("Manager joined before cancellation")
			}
			cancel()
			if !control {
				if err := manager.Shutdown(initial); !errors.Is(err, context.Canceled) {
					t.Fatalf("actual Manager canceled wait: %v", err)
				}
				t.Log("actual Manager canceled wait observed while callback held")
			}
			diagnostic := make(chan error, 1)
			drain := &terminalFixtureWaitBarrier{Context: ctx, entered: make(chan struct{})}
			result := make(chan error, 1)
			go func() { result <- f.finish(initial, drain, func(err error) { diagnostic <- err }) }()
			initialErr := terminalFixtureAwait(t, ctx, diagnostic)
			if !control {
				if !errors.Is(initialErr, context.Canceled) {
					t.Errorf("held wait lost cancellation: %v", initialErr)
				}
				if _, err := st.ListSessions(); err != nil {
					t.Errorf("Store unusable while callback held: %v", err)
				}
				select {
				case <-drain.entered:
					select {
					case <-attempted:
						t.Error("StoreClose attempted during held callback")
					default:
					}
				case <-attempted:
					t.Error("StoreClose attempted before actual Manager join")
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if tc.expired {
					// The same held finalizer must survive an exhausted outer budget.
					expired, expire := context.WithCancelCause(context.Background())
					expire(context.DeadlineExceeded)
					timeoutErr := f.finish(expired, expired, nil)
					if !errors.Is(timeoutErr, context.DeadlineExceeded) || !strings.Contains(timeoutErr.Error(), "retaining") {
						t.Errorf("incomplete drain verdict: %v", timeoutErr)
					}
					select {
					case <-attempted:
						t.Error("incomplete drain attempted StoreClose")
					default:
					}
					if _, err := os.Stat(root); err != nil {
						t.Errorf("owned root not retained: %v", err)
					}
					if _, err := st.ListSessions(); err != nil {
						t.Errorf("retained Store unusable: %v", err)
					}
				}
				unblock()
			} else if initialErr != nil {
				t.Errorf("completed Manager lost to cancellation: %v", initialErr)
			}
			finalErr := terminalFixtureAwait(t, ctx, result)
			closed = true
			if tc.callbackErr == nil && finalErr != nil || tc.callbackErr != nil && (!errors.Is(finalErr, tc.callbackErr) || finalErr.Error() != "session held-exit exit callback: "+tc.callbackErr.Error()) {
				t.Errorf("final teardown: %v, want %v", finalErr, tc.callbackErr)
			}
			if errors.Is(finalErr, context.Canceled) {
				t.Error("diagnostic cancellation polluted final verdict")
			}
			if tc.callbackErr == nil {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Errorf("completed fixture root not removed: %v", err)
				}
			}
		})
	}
}

// Server.Close must stop acceptance without closing the raw pipe endpoints:
// terminalPipeListener.Close remains the later fallback owned by startHTTP.
type terminalFixtureHTTPListener struct {
	*terminalPipeListener
	conn    *terminalFixtureHTTPConn
	stopped chan struct{}
	once    sync.Once
}

func (l *terminalFixtureHTTPListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.incoming:
		l.conn.Conn = conn
		return l.conn, nil
	case <-l.stopped:
		return nil, net.ErrClosed
	}
}

func (l *terminalFixtureHTTPListener) Close() error {
	l.once.Do(func() { close(l.stopped) })
	return nil
}

type terminalFixtureHTTPConn struct {
	net.Conn
	closing, allowClose, closed chan struct{}
	wrote                       chan struct{}
	closeOnce, writeOnce        sync.Once
	closeErr                    error
}

func (c *terminalFixtureHTTPConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closing)
		<-c.allowClose
		c.closeErr = c.Conn.Close()
		close(c.closed)
	})
	return c.closeErr
}

func (c *terminalFixtureHTTPConn) Write(p []byte) (int, error) {
	c.writeOnce.Do(func() { close(c.wrote) })
	return c.Conn.Write(p)
}

func TestTerminalFixtureHTTPDrain(t *testing.T) {
	for _, upgraded := range []bool{false, true} {
		name := "request"
		if upgraded {
			name = "upgraded-events"
		}
		t.Run(name, func(t *testing.T) {
			ctx := terminalFixtureTestContext(t)
			root := terminalFixtureRoot(t)
			st, err := store.Open(filepath.Join(root, "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			manager := session.New(root)
			svc := service.New(st, manager, nil)
			base := (&api.Server{Svc: svc, Token: "http-drain"}).Handler()
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
			transport := &http.Transport{DialContext: listener.dial}
			f := &terminalFixtureLifecycle{manager: manager, listener: listener, transport: transport, root: root}
			var serving net.Listener = listener
			observed := &terminalFixtureHTTPConn{
				closing: make(chan struct{}), allowClose: make(chan struct{}), closed: make(chan struct{}), wrote: make(chan struct{}),
			}
			var closeOnce sync.Once
			allowClose := func() { closeOnce.Do(func() { close(observed.allowClose) }) }
			if !upgraded {
				serving = &terminalFixtureHTTPListener{terminalPipeListener: listener, conn: observed, stopped: make(chan struct{})}
			}
			f.server = &http.Server{Handler: f.handlers.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if upgraded {
					close(entered)
				}
				base.ServeHTTP(w, r)
				if !upgraded {
					close(entered)
				}
				<-release
				_, err := st.ListSessions()
				returned <- err
			}))}
			served := make(chan error, 1)
			f.served = served
			go func() { served <- f.server.Serve(serving) }()
			attempted := make(chan struct{})
			f.closeStore = func() error {
				close(attempted)
				select {
				case err := <-returned:
					if err != nil {
						return err
					}
				default:
					return errors.New("StoreClose before HTTP handler returned")
				}
				select {
				case <-f.httpJoin.done:
				default:
					return errors.New("StoreClose before HTTP joined")
				}
				t.Log("actual Serve, admitted handler and upgraded stream joined before StoreClose")
				return st.Close()
			}
			closed := false
			t.Cleanup(func() {
				allowClose()
				unblock()
				if !closed {
					f.closeStore = st.Close
					if err := f.finish(ctx, ctx, nil); err != nil {
						t.Error(err)
					}
				}
			})
			c := client.New("http://terminal-fixture", "http-drain")
			c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second}
			requestDone := make(chan error, 1)
			if upgraded {
				stream, err := c.DialEvents(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = stream.Close() })
			} else {
				go func() { _, err := c.ListSessions(ctx); requestDone <- err }()
			}
			terminalFixtureAwait(t, ctx, entered)
			f.startManager()
			terminalFixtureAwait(t, ctx, f.managerJoin.done)
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			drain := &terminalFixtureWaitBarrier{Context: ctx, entered: make(chan struct{})}
			result := make(chan error, 1)
			go func() { result <- f.finish(canceled, drain, nil) }()
			terminalFixtureAwait(t, ctx, drain.entered)
			if !upgraded {
				terminalFixtureAwait(t, ctx, observed.closing)
				select {
				case <-observed.closed:
					t.Fatal("connection closed through the held Close gate")
				default:
				}
				select {
				case <-listener.done:
					t.Fatal("listener fallback ran before actual Server.Close completed")
				default:
				}
				select {
				case <-observed.wrote:
					t.Fatal("held response reached the connection before Server.Close")
				default:
				}
				t.Log("drain wait entered while actual server endpoint Close is held; response unwritten")
			}
			// HTTP must get its own wait despite the canceled diagnostic context.
			select {
			case <-attempted:
				t.Error("held handler lost its Store")
			default:
			}
			if _, err := st.ListSessions(); err != nil {
				t.Fatal(err)
			}
			// An exhausted outer budget is also a hard failure while HTTP retains Store.
			expired, expire := context.WithCancelCause(context.Background())
			expire(context.DeadlineExceeded)
			if err := f.finish(expired, expired, nil); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("held HTTP lost deadline failure: %v", err)
			}
			if _, err := os.Stat(root); err != nil {
				t.Errorf("HTTP dependency root removed: %v", err)
			}
			select {
			case <-attempted:
				t.Error("expired HTTP wait closed Store")
			default:
			}
			if !upgraded {
				select {
				case err := <-requestDone:
					t.Fatalf("held request completed before actual connection close: %v", err)
				default:
				}
				allowClose()
				terminalFixtureAwait(t, ctx, observed.closed)
				if observed.closeErr != nil {
					t.Fatal(observed.closeErr)
				}
				// Only Server.Close can reach this wrapper while the handler is held;
				// listener fallback and client cleanup own the unwrapped endpoints.
				select {
				case <-observed.wrote:
					t.Fatal("held response reached the connection before close completed")
				default:
				}
				t.Log("actual server endpoint Close completed before handler release; response unwritten")
			}
			unblock()
			if err := terminalFixtureAwait(t, ctx, result); err != nil {
				t.Error(err)
			}
			closed = true
			if !upgraded {
				// Server.Close interrupts this intentionally held response.
				if err := terminalFixtureAwait(t, ctx, requestDone); err == nil {
					t.Error("held response unexpectedly completed before Server.Close")
				}
			}
		})
	}
}

type terminalFixtureAcceptFailure struct {
	net.Listener
	err error
}

func (l terminalFixtureAcceptFailure) Accept() (net.Conn, error) { return nil, l.err }

func TestTerminalFixtureCompletedJoinsBeatCancellation(t *testing.T) {
	for _, failServe := range []bool{false, true} {
		name := "completed"
		if failServe {
			name = "serve-error"
		}
		t.Run(name, func(t *testing.T) {
			ctx := terminalFixtureTestContext(t)
			root := terminalFixtureRoot(t)
			st, err := store.Open(filepath.Join(root, "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			manager := session.New(root)
			svc := service.New(st, manager, nil)
			listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
			transport := &http.Transport{DialContext: listener.dial}
			f := &terminalFixtureLifecycle{manager: manager, listener: listener, transport: transport, root: root, closeStore: st.Close}
			f.server = &http.Server{Handler: f.handlers.wrap((&api.Server{Svc: svc, Token: "completed"}).Handler())}
			serveFailure := errors.New("fixture listener accept failed")
			var serving net.Listener = listener
			if failServe {
				serving = terminalFixtureAcceptFailure{Listener: listener, err: serveFailure}
			}
			served := make(chan error, 1)
			serveReturned := make(chan struct{})
			f.served = served
			go func() { served <- f.server.Serve(serving); close(serveReturned) }()
			t.Cleanup(func() {
				err := f.finish(ctx, ctx, nil)
				if failServe {
					if err == nil || err.Error() != serveFailure.Error() {
						t.Errorf("cleanup lost Serve error or added failure: %v", err)
					}
				} else if err != nil {
					t.Error(err)
				}
				if _, joined := f.managerJoin.wait(ctx); joined {
					if _, joined := f.httpJoin.wait(ctx); joined {
						if err := os.RemoveAll(root); err != nil {
							t.Error(err)
						}
					}
				}
			})
			if failServe {
				terminalFixtureAwait(t, ctx, serveReturned)
			} else {
				c := client.New("http://terminal-fixture", "completed")
				c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second}
				if _, err := c.ListSessions(ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.startManager()
			terminalFixtureAwait(t, ctx, f.managerJoin.done)
			f.startHTTP()
			terminalFixtureAwait(t, ctx, f.httpJoin.done)
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			err = f.finish(canceled, canceled, nil)
			if failServe {
				if !errors.Is(err, serveFailure) || err.Error() != serveFailure.Error() {
					t.Errorf("actual Serve error lost: %v", err)
				}
			} else if err != nil {
				t.Errorf("completed joins lost to simultaneous cancellation: %v", err)
			}
		})
	}
}
