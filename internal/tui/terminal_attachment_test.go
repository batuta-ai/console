package tui

import (
	"bytes"
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/charmbracelet/x/ansi"
	"net"
	"net/http"
	"path/filepath"
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

// net.Pipe exercises the real HTTP, WebSocket, client, API, store and PTY paths
// without claiming native TCP qualification in a bind-restricted worker.
type terminalPipeListener struct {
	incoming    chan net.Conn
	done        chan struct{}
	once        sync.Once
	mu          sync.Mutex
	connections []net.Conn
}

func (l *terminalPipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.incoming:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *terminalPipeListener) Close() error {
	l.once.Do(func() {
		close(l.done)
		l.mu.Lock()
		defer l.mu.Unlock()
		for _, c := range l.connections {
			_ = c.Close()
		}
	})
	return nil
}
func (l *terminalPipeListener) Addr() net.Addr { return terminalPipeAddr("terminal-fixture") }

type terminalPipeAddr string

func (a terminalPipeAddr) Network() string { return "pipe" }
func (a terminalPipeAddr) String() string  { return string(a) }
func (l *terminalPipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	a, b := net.Pipe()
	l.mu.Lock()
	l.connections = append(l.connections, a, b)
	l.mu.Unlock()
	select {
	case l.incoming <- b:
		return a, nil
	case <-ctx.Done():
		_ = a.Close()
		_ = b.Close()
		return nil, ctx.Err()
	case <-l.done:
		_ = a.Close()
		_ = b.Close()
		return nil, net.ErrClosed
	}
}
func terminalFixture(t *testing.T) (*client.Client, *session.Manager) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager := session.New(root)
	svc := service.New(st, manager, nil)
	listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: (&api.Server{Svc: svc, Token: "disposable-terminal-fixture"}).Handler()}
	joined := make(chan error, 1)
	go func() { joined <- server.Serve(listener) }()
	httpTransport := &http.Transport{DialContext: listener.dial}
	c := client.New("http://terminal-fixture", "disposable-terminal-fixture")
	c.HTTP = &http.Client{Transport: httpTransport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		_ = server.Close()
		_ = listener.Close()
		httpTransport.CloseIdleConnections()
		select {
		case err := <-joined:
			if err != http.ErrServerClosed && err != net.ErrClosed {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("HTTP server did not join")
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, id := range []string{"a-unrelated", "z-selected"} {
		pending, err := manager.StartPending(domain.Session{ID: id, Cols: 80, Rows: 24, Argv: testfixture.Argv(t, []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec /bin/cat"})}, testfixture.TerminalEnvironment(t, []string{"HOME=" + root}), root, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pending.Persist(st.PutSession); err != nil {
			t.Fatal(err)
		}
	}
	return c, manager
}
func TestTerminalExplicitSelectedViewDoesNotTake(t *testing.T) {
	c, manager := terminalFixture(t)
	m := New(c).(model)
	m.sessID = "z-selected"
	result := m.attach()()
	attached, ok := result.(attachedMsg)
	if !ok {
		t.Fatalf("attachment failed: %#v", result)
	}
	if attached.link != nil {
		t.Cleanup(func() { attached.link.cancel(); <-attached.link.done })
	}
	if attached.id != "z-selected" {
		t.Errorf("attached %q instead of exact selected session", attached.id)
	}
	for _, id := range []string{"a-unrelated", "z-selected"} {
		s, ok := manager.Snapshot(id)
		if !ok || s.OwnerClient != "" || s.Owner != "none" {
			t.Errorf("view changed ownership: %+v", s)
		}
	}
}

func terminalAwaitFrame(t *testing.T, l *termLink, accept func(terminalFrame) bool) terminalFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		l.mu.Lock()
		f := l.frame
		l.mu.Unlock()
		if accept(f) {
			return f
		}
		select {
		case <-l.updates:
		case <-l.done:
			t.Fatalf("attachment closed: %s", f.problem)
		case <-ctx.Done():
			t.Fatalf("attachment state timeout: %+v", f)
		}
	}
}
func terminalAttachedFixture(t *testing.T) (model, *session.Manager) {
	t.Helper()
	c, manager := terminalFixture(t)
	m := New(c).(model)
	m.sessID = "z-selected"
	m.width = 80
	m.height = 34
	msg := m.attach()().(attachedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	next, _ := m.Update(msg)
	m = next.(model)
	t.Cleanup(func() {
		m.link.cancel()
		select {
		case <-m.link.done:
		case <-time.After(5 * time.Second):
			t.Error("attachment failed to join")
		}
	})
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.cols == 80 && strings.Contains(ansi.Strip(f.text), "READY") })
	return m, manager
}
func TestTerminalControlAndJoinedDetach(t *testing.T) {
	m, manager := terminalAttachedFixture(t)
	if !m.link.offer(terminalAction{operation: "tui"}) {
		t.Fatal("take rejected")
	}
	f := terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	if f.owner != m.clientID {
		t.Fatalf("wrong actual owner: %q want %q", f.owner, m.clientID)
	}
	m.termMode = true
	next, cmd := m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if next.(model).termMode || cmd == nil {
		t.Fatal("local detach did not start")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("detach did not join")
	}
	for _, id := range []string{"a-unrelated", "z-selected"} {
		s, ok := manager.Snapshot(id)
		if !ok || s.Process != "running" || s.OwnerClient != "" {
			t.Fatalf("detach changed child or retained control: %+v", s)
		}
	}
}
func TestTerminalStaleGenerationRejected(t *testing.T) {
	m := terminalTestModel()
	m.generation = 2
	m.clientID = "actual"
	for _, msg := range []terminalFrameMsg{
		{sessionID: m.sessID, clientID: "actual", generation: 1, frame: terminalFrame{text: "STALE"}},
		{sessionID: "other", clientID: "actual", generation: 2, frame: terminalFrame{text: "STALE"}},
		{sessionID: m.sessID, clientID: "other", generation: 2, frame: terminalFrame{text: "STALE"}},
	} {
		next, _ := m.Update(msg)
		if next.(model).termFrame.text != "" {
			t.Fatal("stale frame accepted")
		}
	}
}
func TestTerminalFocusedInputExact(t *testing.T) {
	m, _ := terminalAttachedFixture(t)
	m.link.offer(terminalAction{operation: "tui"})
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	m.termMode = true
	next, _ := m.Update(tea.PasteMsg{Content: "{\"type\":\"resize\",\"cols\":3,\"rows\":2}\n"})
	m = next.(model)
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(ansi.Strip(f.text), "\"type\":\"resize\"") })
	m.link.mu.Lock()
	f := m.link.frame
	m.link.mu.Unlock()
	if f.cols != 80 || f.rows != 24 {
		t.Fatalf("literal paste resized child: %dx%d", f.cols, f.rows)
	}
}

func TestTerminalReadonlyDetach(t *testing.T) {
	m, _ := terminalAttachedFixture(t)
	next, cmd := m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if next.(model).termMode || cmd == nil {
		t.Fatal("read-only attachment cannot detach from panel focus")
	}
	done := make(chan struct{})
	go func() { cmd(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("read-only detach did not join")
	}
}

func TestTerminalResizeStormKeepsLatest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &termLink{ctx: ctx, cancel: cancel, inbox: newTerminalMailbox[terminalAction]()}
	for i := 0; i < 10000; i++ {
		if !l.offer(terminalAction{cols: 80 + i%400, rows: 24 + i%170}) {
			t.Fatalf("resize storm overflow at %d", i)
		}
	}
	l.inbox.mu.Lock()
	n := len(l.inbox.items)
	l.inbox.mu.Unlock()
	if n != 1 {
		t.Fatalf("pending resize events=%d, want 1", n)
	}
}

func TestTerminalActualUpdateRawControlBytes(t *testing.T) {
	m, manager := terminalAttachedFixture(t)
	m.link.offer(terminalAction{operation: "tui"})
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	m.termMode = true
	sub, err := manager.SubscribeTerminal(m.sessID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var replay []byte
	for !strings.Contains(string(replay), "READY") {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		replay = append(replay, e.Data...)
	}
	var want []byte
	for _, key := range []tea.KeyPressMsg{
		{Code: 'q', Text: "q"}, {Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}, {Code: 'z', Mod: tea.ModCtrl}, {Code: '\\', Mod: tea.ModCtrl}, {Code: 'd', Mod: tea.ModCtrl},
	} {
		next, _ := m.Update(key)
		m = next.(model)
	}
	want = []byte{'q', 27, 3, 26, 28, 4}
	var got []byte
	for len(got) < len(want) {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Data...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("child bytes=%x want=%x", got, want)
	}
	if s, ok := manager.Snapshot(m.sessID); !ok || s.Process != "running" {
		t.Fatal("raw control keys stopped child")
	}
}
func TestTerminalKeyboardModesAndPaste(t *testing.T) {
	for _, tc := range []struct {
		key         tea.KeyPressMsg
		application bool
		want        string
	}{
		{tea.KeyPressMsg{Code: tea.KeyUp}, false, "\x1b[A"},
		{tea.KeyPressMsg{Code: tea.KeyUp}, true, "\x1bOA"},
		{tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl | tea.ModShift}, true, "\x1b[1;6D"},
		{tea.KeyPressMsg{Code: tea.KeyPgUp}, false, "\x1b[5~"},
		{tea.KeyPressMsg{Code: 'é', Text: "é", Mod: tea.ModAlt}, false, "\x1bé"},
	} {
		got, err := encodeTerminalKey(tc.key, terminalObserver{application: tc.application})
		if err != nil || string(got) != tc.want {
			t.Fatalf("%s: %q != %q (%v)", tc.key.String(), got, tc.want, err)
		}
	}
	for _, bracketed := range []bool{false, true} {
		got, err := encodeTerminalPaste("é", bracketed)
		want := "é"
		if bracketed {
			want = "\x1b[200~é\x1b[201~"
		}
		if err != nil || string(got) != want {
			t.Fatalf("paste=%q: %v", got, err)
		}
		got, err = encodeTerminalPaste(strings.Repeat("x", (1<<20)+1), bracketed)
		if err == nil || len(got) != 0 {
			t.Fatal("oversized paste partially encoded")
		}
	}
}

func TestTerminalInputBudgetIncludesInflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &termLink{ctx: ctx, cancel: cancel, owned: true, writes: newTerminalMailbox[terminalWrite]()}
	if !l.outgoing(make([]byte, 1<<20), 0, 0) {
		t.Fatal("exact pending bound rejected")
	}
	if _, ok := l.writes.pop(ctx); !ok {
		t.Fatal("cannot take in-flight write")
	}
	if l.outgoing([]byte("x"), 0, 0) {
		t.Fatal("in-flight bytes stopped counting toward the input bound")
	}
}

func TestRootTerminalOwnershipPollAfterTake(t *testing.T) {
	m, manager := terminalAttachedFixture(t)
	if !m.link.offer(terminalAction{operation: "tui"}) {
		t.Fatal("take rejected")
	}
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	foreign, err := m.api.ControlSession(ctx, m.sessID, client.ControlSessionRequest{Surface: "web", Client: "second-native-client"})
	if err != nil || foreign.OwnerClient != "second-native-client" {
		t.Fatalf("foreign take %+v %v", foreign, err)
	}
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return !f.owned && f.owner == "second-native-client" })
	for _, id := range []string{"a-unrelated", "z-selected"} {
		if snap, ok := manager.Snapshot(id); !ok || snap.Process != "running" {
			t.Fatalf("lost child %+v", snap)
		}
	}
}
