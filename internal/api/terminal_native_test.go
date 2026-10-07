//go:build darwin || linux

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
)

type terminalChildCommand struct {
	Output []byte
	Read   int
	Exit   bool
}

func TestTerminalWireChild(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "terminal-wire-child" {
		return
	}
	commands, err := os.Open(os.Args[len(os.Args)-2])
	if err != nil {
		panic(err)
	}
	ack, err := os.OpenFile(os.Args[len(os.Args)-1], os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	stty := exec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	if err = stty.Run(); err != nil {
		panic(err)
	}
	if _, err = ack.Write([]byte{1}); err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(commands)
	for {
		var command terminalChildCommand
		if err = decoder.Decode(&command); err != nil {
			panic(err)
		}
		if command.Exit {
			os.Exit(0)
		}
		data := command.Output
		if command.Read > 0 {
			data = make([]byte, command.Read)
			if _, err = io.ReadFull(os.Stdin, data); err != nil {
				panic(err)
			}
		}
		if len(data) > 0 {
			if _, err = os.Stdout.Write(data); err != nil {
				panic(err)
			}
		}
		if _, err = ack.Write([]byte{1}); err != nil {
			panic(err)
		}
	}
}

type terminalNativeFixture struct {
	svc           *service.Service
	id            string
	commands, ack *os.File
	observer      *session.TerminalSubscription
}

func terminalFixture(t *testing.T, svc *service.Service, id string) *terminalNativeFixture {
	t.Helper()
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "commands"), filepath.Join(dir, "ack")}
	files := make([]*os.File, 2)
	for i, path := range paths {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(fd), path)
		files[i] = f
		t.Cleanup(func() { f.Close() })
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Session.Start(domain.Session{ID: id, Cols: 80, Rows: 24, Argv: []string{exe, "-test.run=^TestTerminalWireChild$", "--", "terminal-wire-child", paths[0], paths[1]}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, ok := svc.Session.Live(id); ok {
			if _, err := svc.Session.Stop(id); err != nil {
				t.Error(err)
			}
		}
	})
	sub, err := svc.Session.SubscribeTerminal(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	f := &terminalNativeFixture{svc, id, files[0], files[1], sub}
	f.waitAck(t)
	return f
}
func (f *terminalNativeFixture) waitAck(t *testing.T) {
	t.Helper()
	if err := f.ack.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := io.ReadFull(f.ack, b[:]); err != nil {
		t.Fatal(err)
	}
}
func (f *terminalNativeFixture) send(t *testing.T, command terminalChildCommand) {
	t.Helper()
	if err := f.commands.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f.commands).Encode(command); err != nil {
		t.Fatal(err)
	}
}
func (f *terminalNativeFixture) emit(t *testing.T, ctx context.Context, data []byte) {
	t.Helper()
	f.send(t, terminalChildCommand{Output: data})
	f.waitAck(t)
	var got []byte
	for len(got) < len(data) {
		e, err := f.observer.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Data...)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("PTY output differs: %d/%d", len(got), len(data))
	}
}
func terminalContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func terminalReadTyped(t *testing.T, ctx context.Context, c *client.Terminal, want []byte) {
	t.Helper()
	var got []byte
	for len(got) < len(want) {
		e, err := c.Read(ctx)
		if err != nil || e.Data == nil {
			t.Fatalf("binary stream: %+v %v", e, err)
		}
		got = append(got, e.Data...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ordered bytes differ: %d/%d", len(got), len(want))
	}
}
func terminalGeometry(t *testing.T, ctx context.Context, c *client.Terminal, cols, rows int) {
	t.Helper()
	e, err := c.Read(ctx)
	if err != nil || e.Data != nil || e.Cols != cols || e.Rows != rows {
		t.Fatalf("geometry: %+v %v, want %dx%d", e, err, cols, rows)
	}
}

func TestTerminalNativeTypedReplayLiveResize(t *testing.T) {
	for _, size := range []int{0, 40 << 10, 64 << 10, 72 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := terminalContext(t)
			h, svc := testServer(t)
			fixture := terminalFixture(t, svc, "selected")
			other := terminalFixture(t, svc, "unrelated")
			if _, err := svc.Session.Take("selected", "tui", "owner"); err != nil {
				t.Fatal(err)
			}
			replay := make([]byte, size)
			for i := range replay {
				replay[i] = byte(i % 251)
			}
			fixture.emit(t, ctx, replay)
			ts, httpClient := terminalTestServer(t, h)
			api := client.New(ts.URL, "test-token")
			api.HTTP = httpClient
			owner, err := api.DialTerminalV1(ctx, "selected", "owner")
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			viewer, err := api.DialTerminalV1(ctx, "selected", "different-viewport")
			if err != nil {
				t.Fatal(err)
			}
			defer viewer.Close()
			terminalGeometry(t, ctx, owner, 80, 24)
			terminalGeometry(t, ctx, viewer, 80, 24)
			// Replay has been subscribed, but neither socket has consumed it yet.
			live := []byte("LIVE-after-replay")
			fixture.emit(t, ctx, live)
			want := append(append([]byte{}, replay[max(0, len(replay)-(64<<10)):]...), live...)
			terminalReadTyped(t, ctx, owner, want)
			terminalReadTyped(t, ctx, viewer, want)
			if err := owner.Resize(ctx, 97, 31); err != nil {
				t.Fatal(err)
			}
			terminalGeometry(t, ctx, owner, 97, 31)
			terminalGeometry(t, ctx, viewer, 97, 31)
			fixture.emit(t, ctx, []byte("AFTER-RESIZE"))
			terminalReadTyped(t, ctx, owner, []byte("AFTER-RESIZE"))
			terminalReadTyped(t, ctx, viewer, []byte("AFTER-RESIZE"))
			input := bytes.Repeat([]byte("literal-input-"), 10000)
			fixture.send(t, terminalChildCommand{Read: len(input)})
			if err := owner.Input(ctx, input); err != nil {
				t.Fatal(err)
			}
			fixture.waitAck(t)
			terminalReadTyped(t, ctx, owner, input)
			terminalReadTyped(t, ctx, viewer, input)
			snap, ok := svc.Session.Snapshot("selected")
			if !ok || snap.OwnerClient != "owner" || snap.Cols != 97 || snap.Rows != 31 {
				t.Fatalf("ownership/geometry %+v", snap)
			}
			other.emit(t, ctx, []byte("OTHER-ALIVE"))
			// Joined detach followed by a fresh attach must not take control or stop it.
			owner.Close()
			viewer.Close()
			again, err := api.DialTerminalV1(ctx, "selected", "reconnected")
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			terminalGeometry(t, ctx, again, 97, 31)
			snap, _ = svc.Session.Snapshot("selected")
			if snap.OwnerClient != "owner" || snap.Process != "running" {
				t.Fatalf("detach changed ownership/lifecycle: %+v", snap)
			}
		})
	}
}

func TestTerminalNativeLegacyResizeAndForeignOwner(t *testing.T) {
	for _, protocol := range []string{"", session.TerminalProtocol} {
		t.Run("protocol="+protocol, func(t *testing.T) {
			ctx := terminalContext(t)
			h, svc := testServer(t)
			fixture := terminalFixture(t, svc, "selected")
			other := terminalFixture(t, svc, "unrelated")
			if _, err := svc.Session.Take("selected", "web", "owner"); err != nil {
				t.Fatal(err)
			}
			ts, httpClient := terminalTestServer(t, h)
			dial := func(who string) *websocket.Conn {
				options := &websocket.DialOptions{HTTPClient: httpClient}
				if protocol != "" {
					options.Subprotocols = []string{protocol}
				}
				c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/sessions/selected/terminal?client="+who+"&token=test-token", options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { c.CloseNow() })
				return c
			}
			owner := dial("owner")
			foreign := dial("foreign")
			type frame struct {
				typ  websocket.MessageType
				data []byte
				err  error
			}
			read := func(c *websocket.Conn) (chan frame, chan struct{}) {
				ch := make(chan frame, 16)
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						typ, b, err := c.Read(ctx)
						select {
						case ch <- frame{typ, b, err}:
						case <-ctx.Done():
							return
						}
						if err != nil {
							return
						}
					}
				}()
				return ch, done
			}
			ownFrames, ownDone := read(owner)
			foreignFrames, foreignDone := read(foreign)
			defer func() { owner.CloseNow(); foreign.CloseNow(); <-ownDone; <-foreignDone }()
			next := func(ch chan frame) frame {
				select {
				case f := <-ch:
					return f
				case <-ctx.Done():
					t.Fatal("frame deadline")
					return frame{}
				}
			}
			if protocol != "" {
				for _, ch := range []chan frame{ownFrames, foreignFrames} {
					f := next(ch)
					if f.err != nil || f.typ != websocket.MessageText || string(f.data) != `{"type":"geometry","cols":80,"rows":24}` {
						t.Fatalf("initial %+v", f)
					}
				}
			}
			if err := foreign.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":123,"rows":45}`)); err != nil {
				t.Fatal(err)
			}
			if err := foreign.Write(ctx, websocket.MessageBinary, []byte("FORBIDDEN")); err != nil {
				t.Fatal(err)
			}
			if err := foreign.Ping(ctx); err != nil {
				t.Fatalf("foreign resize must stay silent: %v", err)
			}
			if protocol == "" {
				for _, data := range []string{`{"type":"resize","cols":0,"rows":24}`, `{"type":"resize","cols":65536,"rows":24}`} {
					if err := owner.Write(ctx, websocket.MessageText, []byte(data)); err != nil {
						t.Fatal(err)
					}
				}
				if err := owner.Ping(ctx); err != nil {
					t.Fatalf("invalid legacy resize closed: %v", err)
				}
			}
			snap, _ := svc.Session.Snapshot("selected")
			if snap.Cols != 80 || snap.Rows != 24 || snap.OwnerClient != "owner" {
				t.Fatalf("foreign/invalid mutation: %+v", snap)
			}
			literal := []byte(`{"type":"resize","cols":97,"rows":31}`)
			fixture.send(t, terminalChildCommand{Read: len(literal)})
			if err := owner.Write(ctx, websocket.MessageBinary, literal); err != nil {
				t.Fatal(err)
			}
			fixture.waitAck(t)
			for _, ch := range []chan frame{ownFrames, foreignFrames} {
				var got []byte
				for len(got) < len(literal) {
					f := next(ch)
					if f.err != nil || f.typ != websocket.MessageBinary {
						t.Fatalf("literal frame %+v", f)
					}
					got = append(got, f.data...)
				}
				if !bytes.Equal(got, literal) {
					t.Fatalf("literal/foreign input: %q", got)
				}
			}
			if err := owner.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":97,"rows":31}`)); err != nil {
				t.Fatal(err)
			}
			if err := owner.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			if protocol != "" {
				for _, ch := range []chan frame{ownFrames, foreignFrames} {
					f := next(ch)
					if f.err != nil || f.typ != websocket.MessageText || string(f.data) != `{"type":"geometry","cols":97,"rows":31}` {
						t.Fatalf("resize %+v", f)
					}
				}
			}
			// A byte barrier on both sockets proves the legacy wire has no geometry.
			fixture.send(t, terminalChildCommand{Output: []byte("BARRIER")})
			fixture.waitAck(t)
			for _, ch := range []chan frame{ownFrames, foreignFrames} {
				var got []byte
				for len(got) < len("BARRIER") {
					f := next(ch)
					if f.err != nil || f.typ != websocket.MessageBinary {
						t.Fatalf("post-resize %+v", f)
					}
					got = append(got, f.data...)
				}
				if string(got) != "BARRIER" {
					t.Fatalf("post-resize bytes %q", got)
				}
			}
			other.emit(t, ctx, []byte("ALIVE"))
			fixture.send(t, terminalChildCommand{Exit: true})
			for _, ch := range []chan frame{ownFrames, foreignFrames} {
				f := next(ch)
				if websocket.CloseStatus(f.err) != websocket.StatusNormalClosure {
					t.Fatalf("child exit close: %v", f.err)
				}
			}
		})
	}
}

func TestTerminalNativeAuthenticationAndNegotiation(t *testing.T) {
	ctx := terminalContext(t)
	h, svc := testServer(t)
	terminalFixture(t, svc, "exact")
	ts, httpClient := terminalTestServer(t, h)
	for _, tc := range []struct {
		token     string
		protocols []string
		status    int
		code      string
	}{
		{"", []string{"unknown"}, 401, "unauthorized"},
		{"test-token", []string{"unknown"}, 400, "unsupported_terminal_protocol"},
		{"test-token", []string{"unknown", session.TerminalProtocol}, 101, ""},
		{"test-token", nil, 101, ""},
	} {
		c, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/sessions/exact/terminal?client=viewer&token="+tc.token, &websocket.DialOptions{HTTPClient: httpClient, Subprotocols: tc.protocols})
		if response == nil || response.StatusCode != tc.status {
			t.Fatalf("status: %+v %v", response, err)
		}
		if tc.status != 101 {
			if err == nil || c != nil {
				t.Fatalf("unexpected upgrade: %v", err)
			}
			b, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if !bytes.Contains(b, []byte(tc.code)) {
				t.Fatalf("error envelope %s", b)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if len(tc.protocols) > 0 {
			want = session.TerminalProtocol
		}
		if c.Subprotocol() != want {
			t.Fatalf("protocol %q", c.Subprotocol())
		}
		c.CloseNow()
	}
	snap, _ := svc.Session.Snapshot("exact")
	if snap.OwnerClient != "" {
		t.Fatalf("view acquired control: %+v", snap)
	}
}

// The native TCP writer can briefly regain capacity while a legacy consumer
// discards geometry events. This test-owned barrier holds that real writer
// through the event flood after native kernel backpressure has been observed.
type terminalControlledTCPConn struct {
	*net.TCPConn
	paused     atomic.Bool
	resume     chan struct{}
	closed     chan struct{}
	resumeOnce sync.Once
	closeOnce  sync.Once
}

func (c *terminalControlledTCPConn) Write(p []byte) (int, error) {
	if c.paused.Load() {
		select {
		case <-c.resume:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.TCPConn.Write(p)
}

func (c *terminalControlledTCPConn) resumeWrites() { c.resumeOnce.Do(func() { close(c.resume) }) }
func (c *terminalControlledTCPConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.TCPConn.Close()
}

type terminalControlledTCPListener struct{ net.Listener }

func (l terminalControlledTCPListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &terminalControlledTCPConn{TCPConn: c.(*net.TCPConn), resume: make(chan struct{}), closed: make(chan struct{})}, nil
}

func TestTerminalNativeOverflowAndWriteBackpressure(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, kind := range []string{"events", "byte-volume", "disconnect"} {
			t.Run(fmt.Sprintf("legacy=%v/%s", legacy, kind), func(t *testing.T) {
				ctx := terminalContext(t)
				h, svc := testServer(t)
				fixture := terminalFixture(t, svc, "slow")
				other := terminalFixture(t, svc, "other")
				if _, err := svc.Session.Take("slow", "tui", "owner"); err != nil {
					t.Fatal(err)
				}
				handlerDone := make(chan struct{}, 1)
				tcpConnections := make(chan *terminalControlledTCPConn, 1)
				ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer func() { handlerDone <- struct{}{} }()
					h.ServeHTTP(w, r)
				}))
				ts.Listener = terminalControlledTCPListener{ts.Listener}
				ts.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
					tcp := c.(*terminalControlledTCPConn)
					if err := tcp.SetWriteBuffer(4096); err != nil {
						t.Error(err)
					}
					tcpConnections <- tcp
					return ctx
				}
				ts.Start()
				defer ts.Close()
				transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					c, err := (&net.Dialer{}).DialContext(ctx, network, address)
					if err == nil {
						err = c.(*net.TCPConn).SetReadBuffer(4096)
					}
					return c, err
				}}
				defer transport.CloseIdleConnections()
				options := &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}}
				if !legacy {
					options.Subprotocols = []string{session.TerminalProtocol}
				}
				c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/sessions/slow/terminal?client=owner&token=test-token", options)
				if err != nil {
					t.Fatal(err)
				}
				defer c.CloseNow()
				c.SetReadLimit(64 << 10)
				if !legacy {
					typ, b, err := c.Read(ctx)
					if err != nil || typ != websocket.MessageText || string(b) != `{"type":"geometry","cols":80,"rows":24}` {
						t.Fatalf("initial %d %q %v", typ, b, err)
					}
				}
				tcp := <-tcpConnections
				defer tcp.resumeWrites()
				raw, err := tcp.SyscallConn()
				if err != nil {
					t.Fatal(err)
				}
				// Retain real TCP/HTTP/WebSocket implementations. Reduced socket buffers
				// make kernel non-writability observable without a fake listener or sleeps.
				saturated := false
				stacks := make([]byte, 1<<20)
				for i := 0; i < 256; i++ {
					fixture.emit(t, ctx, bytes.Repeat([]byte("x"), 4096))
					var ready int
					var pollErr error
					if err = raw.Control(func(fd uintptr) {
						for {
							ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 0)
							if !errors.Is(pollErr, unix.EINTR) {
								break
							}
							if ctx.Err() != nil {
								pollErr = ctx.Err()
								break
							}
						}
					}); err != nil {
						t.Fatal(err)
					}
					if pollErr != nil {
						t.Fatal(pollErr)
					}
					if ready == 0 {
						n := runtime.Stack(stacks, true)
						for _, goroutine := range bytes.Split(stacks[:n], []byte("\n\n")) {
							if bytes.Contains(goroutine, []byte("api.writeTerminalOutput(")) && bytes.Contains(goroutine, []byte("internal/poll.(*FD).Write(")) && bytes.Contains(goroutine, []byte("[IO wait]")) {
								saturated = true
								t.Logf("observed TCP output writer blocked after %d bytes:\n%s", (i+1)*4096, goroutine)
								break
							}
						}
						if saturated {
							break
						}
					}
				}
				if !saturated {
					t.Fatal("kernel output backpressure was not observed")
				}
				if legacy && kind == "events" {
					tcp.paused.Store(true)
					fixture.emit(t, ctx, []byte("legacy-event-barrier"))
				}
				fixture.observer.Close()
				if kind == "disconnect" {
					_ = c.CloseNow()
				} else if kind == "events" {
					for i := 0; i < 1024; i++ {
						if err = svc.Session.Resize("slow", "owner", 80+i%2, 24); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					fixture.send(t, terminalChildCommand{Output: bytes.Repeat([]byte("y"), 2<<20)})
					fixture.waitAck(t)
				}
				tcp.resumeWrites()
				// Drain only after producing beyond the bounded queue. Close frames must
				// survive the blocked writer, and the handler must join before returning.
				if kind != "disconnect" {
					for {
						_, _, err = c.Read(ctx)
						if err != nil {
							break
						}
					}
					var closeErr websocket.CloseError
					if !errors.As(err, &closeErr) || closeErr.Code != 1013 || closeErr.Reason != "terminal_output_overflow" {
						t.Fatalf("overflow close: %v", err)
					}
				}
				select {
				case <-handlerDone:
				case <-ctx.Done():
					t.Fatal("terminal handler did not join")
				}
				// New output and a fresh subscriber remain usable after overload cleanup.
				resumed, err := svc.Session.SubscribeTerminal("slow")
				if err != nil {
					t.Fatal(err)
				}
				resumed.Close()
				snap, _ := svc.Session.Snapshot("slow")
				if snap.Process != "running" || snap.OwnerClient != "owner" {
					t.Fatalf("overload changed session %+v", snap)
				}
				other.emit(t, ctx, []byte("OTHER-ALIVE"))
			})
		}
	}
}

func TestTerminalNativeRapidAttachDetachAndExit(t *testing.T) {
	ctx := terminalContext(t)
	h, svc := testServer(t)
	fixture := terminalFixture(t, svc, "rapid")
	other := terminalFixture(t, svc, "other")
	done := make(chan struct{}, 1)
	ts, httpClient := terminalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer func() { done <- struct{}{} }(); h.ServeHTTP(w, r) }))
	api := client.New(ts.URL, "test-token")
	api.HTTP = httpClient
	for i := 0; i < 32; i++ {
		c, err := api.DialTerminalV1(ctx, "rapid", fmt.Sprintf("viewer-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		terminalGeometry(t, ctx, c, 80, 24)
		if err = c.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("detach did not join")
		}
		snap, _ := svc.Session.Snapshot("rapid")
		if snap.OwnerClient != "" || snap.Process != "running" {
			t.Fatalf("implicit take/stop %+v", snap)
		}
	}
	c, err := api.DialTerminalV1(ctx, "rapid", "exit-viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	terminalGeometry(t, ctx, c, 80, 24)
	fixture.send(t, terminalChildCommand{Exit: true})
	_, err = c.Read(ctx)
	if websocket.CloseStatus(err) != 1000 {
		t.Fatalf("exit close: %v", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("exit did not join")
	}
	other.emit(t, ctx, []byte("STILL-ALIVE"))
}

// This runs the same disposable child without a listener, independently proving
// the byte and geometry barriers used by the native WebSocket tests.
func TestTerminalNativePTYOrderingBarrier(t *testing.T) {
	ctx := terminalContext(t)
	_, svc := testServer(t)
	fixture := terminalFixture(t, svc, "pty-barrier")
	if _, err := svc.Session.Take(fixture.id, "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	replay := bytes.Repeat([]byte("0123456789abcdef"), 4608)
	fixture.emit(t, ctx, replay)
	sub, err := svc.Session.SubscribeTerminal(fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	event, err := sub.Next(ctx)
	if err != nil || event.Data != nil || event.Cols != 80 || event.Rows != 24 {
		t.Fatalf("geometry %+v %v", event, err)
	}
	var got []byte
	for len(got) < 64<<10 {
		event, err = sub.Next(ctx)
		if err != nil || !event.Replay {
			t.Fatalf("replay %+v %v", event, err)
		}
		got = append(got, event.Data...)
	}
	if !bytes.Equal(got, replay[len(replay)-(64<<10):]) {
		t.Fatal("bounded replay differs")
	}
	fixture.emit(t, ctx, []byte("LIVE"))
	if err = svc.Session.Resize(fixture.id, "owner", 97, 31); err != nil {
		t.Fatal(err)
	}
	event, err = sub.Next(ctx)
	if err != nil || string(event.Data) != "LIVE" {
		t.Fatalf("live %+v %v", event, err)
	}
	event, err = sub.Next(ctx)
	if err != nil || event.Data != nil || event.Cols != 97 || event.Rows != 31 {
		t.Fatalf("resize %+v %v", event, err)
	}
	literal := []byte(`{"type":"resize","cols":13,"rows":9}`)
	fixture.send(t, terminalChildCommand{Read: len(literal)})
	if err = svc.Session.InputContext(ctx, fixture.id, "owner", literal); err != nil {
		t.Fatal(err)
	}
	fixture.waitAck(t)
	got = nil
	for len(got) < len(literal) {
		event, err = sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, event.Data...)
	}
	if !bytes.Equal(got, literal) {
		t.Fatalf("child literal %q", got)
	}
}

func TestTerminalNativeTinyOutputBarriers(t *testing.T) {
	ctx := terminalContext(t)
	h, svc := testServer(t)
	fixture := terminalFixture(t, svc, "tiny")
	if _, err := svc.Session.Take("tiny", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	ts, httpClient := terminalTestServer(t, h)
	api := client.New(ts.URL, "test-token")
	api.HTTP = httpClient
	c, err := api.DialTerminalV1(ctx, "tiny", "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	terminalGeometry(t, ctx, c, 80, 24)
	for i := 0; i < 300; i++ {
		data := []byte{byte(i % 251)}
		fixture.emit(t, ctx, data)
		terminalReadTyped(t, ctx, c, data)
		if i%17 == 0 {
			if err = c.Resize(ctx, 80+i%2, 24); err != nil {
				t.Fatal(err)
			}
			terminalGeometry(t, ctx, c, 80+i%2, 24)
		}
	}
}

func TestTerminalNativeFragmentedOversizedBinary(t *testing.T) {
	ctx := terminalContext(t)
	h, svc := testServer(t)
	terminalFixture(t, svc, "oversize")
	if _, err := svc.Session.Take("oversize", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	ts, httpClient := terminalTestServer(t, h)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/v0/sessions/oversize/terminal?client=owner&token=test-token", &websocket.DialOptions{HTTPClient: httpClient, Subprotocols: []string{session.TerminalProtocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if _, _, err = c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := c.Writer(ctx, websocket.MessageBinary)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 17; i++ {
		if _, err = writer.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
			break
		}
	}
	_ = writer.Close()
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != 1009 {
		t.Fatalf("fragmented logical message close: %v", err)
	}
	snap, _ := svc.Session.Snapshot("oversize")
	if snap.Process != "running" || snap.OwnerClient != "owner" {
		t.Fatalf("oversize stopped child: %+v", snap)
	}
}
