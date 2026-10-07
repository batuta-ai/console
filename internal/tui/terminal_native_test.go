//go:build darwin || linux

package tui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

func terminalNativeAttachment(t *testing.T, selectedArgv []string) (model, *session.Manager) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager := session.New(root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: (&api.Server{Svc: service.New(st, manager, nil), Token: "disposable-native-terminal"}).Handler()}
	joined := make(chan error, 1)
	go func() { joined <- server.Serve(listener) }()
	transport := &http.Transport{}
	c := client.New("http://"+listener.Addr().String(), "disposable-native-terminal")
	c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		server.Close()
		listener.Close()
		transport.CloseIdleConnections()
		select {
		case err := <-joined:
			if err != http.ErrServerClosed && err != net.ErrClosed {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("native HTTP server did not join")
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, id := range []string{"a-unrelated", "z-selected"} {
		argv := []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec /bin/cat"}
		if id == "z-selected" && selectedArgv != nil {
			argv = selectedArgv
		}
		pending, err := manager.StartPending(domain.Session{ID: id, Cols: 80, Rows: 24, Argv: argv}, []string{"HOME=" + root}, root, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pending.Persist(st.PutSession); err != nil {
			t.Fatal(err)
		}
	}
	m := New(c).(model)
	m.screen, m.sessID, m.width, m.height = scrRoom, "z-selected", 80, 34
	attached := m.attach()().(attachedMsg)
	if attached.err != nil {
		t.Fatal(attached.err)
	}
	next, _ := m.Update(attached)
	m = next.(model)
	t.Cleanup(func() { m.lifetime.close() })
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool {
		return f.cols == 80 && f.rows == 24 && strings.Contains(ansi.Strip(f.text), "READY")
	})
	for _, id := range []string{"a-unrelated", "z-selected"} {
		s, ok := manager.Snapshot(id)
		if !ok || s.OwnerClient != "" || s.Process != "running" {
			t.Fatalf("view acquired or changed child: %+v", s)
		}
	}
	return m, manager
}

func terminalNativeTake(t *testing.T, m model) model {
	t.Helper()
	next, _ := m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = next.(model)
	m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned && f.owner == m.clientID })
	next, _ = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = next.(model)
	if !m.termMode {
		t.Fatal("explicit owned input did not focus child")
	}
	return m
}

func terminalNativeOutput(t *testing.T, ctx context.Context, sub *session.TerminalSubscription, marker []byte) []byte {
	t.Helper()
	var got []byte
	for !bytes.Contains(got, marker) {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; output=%q", marker, err, got)
		}
		got = append(got, event.Data...)
	}
	return got
}

func TestTerminalNativeRawInput(t *testing.T) {
	m, manager := terminalNativeAttachment(t, nil)
	m.termMode = true
	next, _ := m.Update(tea.PasteMsg{Content: "FORBIDDEN-READONLY"})
	m = next.(model)
	m.termMode = false
	m = terminalNativeTake(t, m)
	sub, err := manager.SubscribeTerminal(m.sessID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	terminalNativeOutput(t, ctx, sub, []byte("READY"))
	keys := []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}, {Code: 'z', Mod: tea.ModCtrl}, {Code: '\\', Mod: tea.ModCtrl}, {Code: 'd', Mod: tea.ModCtrl}}
	for _, key := range keys {
		next, _ = m.Update(key)
		m = next.(model)
	}
	literal := "{\"type\":\"resize\",\"cols\":3,\"rows\":2}\n"
	next, _ = m.Update(tea.PasteMsg{Content: literal})
	m = next.(model)
	want := append([]byte{'q', 27, 3, 26, 28, 4}, []byte(literal)...)
	got := terminalNativeOutput(t, ctx, sub, []byte(literal))
	if !bytes.Equal(got, want) {
		t.Fatalf("native child bytes=%x want=%x", got, want)
	}
	f := terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(ansi.Strip(f.text), "\"type\":\"resize\"") })
	if f.cols != 80 || f.rows != 24 {
		t.Fatalf("literal binary paste resized child: %dx%d", f.cols, f.rows)
	}
}

func TestTerminalNativeFragmentedPromptFinalGlyph(t *testing.T) {
	m, _ := terminalNativeAttachment(t, nil)
	m = terminalNativeTake(t, m)
	for _, mode := range []string{"h", "l"} {
		for _, glyph := range []string{"e\u0301", "世", "👩‍💻", "🇧🇷"} {
			input := "\x1b[2J\x1b[H\x1b[?2027" + mode + glyph + "X!"
			for _, b := range []byte(input) {
				next, _ := m.Update(tea.PasteMsg{Content: string([]byte{b})})
				m = next.(model)
			}
			m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool {
				return strings.Contains(ansi.Strip(f.text), glyph+"X!")
			})
			if m.termFrame.cursorRow != 0 || m.termFrame.cursorCol != uniseg.StringWidth(glyph+"X!") {
				t.Fatalf("fragmented native cursor for %q: (%d,%d)", glyph, m.termFrame.cursorRow, m.termFrame.cursorCol)
			}
			if !strings.Contains(ansi.Strip(m.View().Content), glyph+"X!") {
				t.Fatalf("actual View withheld native final glyph for %q mode %s", glyph, mode)
			}
		}
	}
}

func TestTerminalNativeUnsupportedGeometryAndRecovery(t *testing.T) {
	m, manager := terminalNativeAttachment(t, nil)
	before := terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "READY") })
	const otherOwner = "native-geometry-owner"
	if _, err := manager.Take(m.sessID, "web", otherOwner); err != nil {
		t.Fatal(err)
	}
	if err := manager.Resize(m.sessID, otherOwner, 80, 1); err != nil {
		t.Fatal(err)
	}
	m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.problem, "unsupported child geometry") })
	if m.termFrame.text != before.text || !strings.Contains(m.View().Content, "unsupported child geometry") {
		t.Fatal("unsupported authoritative size did not visibly freeze the last screen")
	}
	m = terminalNativeTake(t, m)
	if s, ok := manager.Snapshot(m.sessID); !ok || s.Cols != 80 || s.Rows != 1 {
		t.Fatal("take implicitly resized unsupported child")
	}
	sub, err := manager.SubscribeTerminal(m.sessID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	terminalNativeOutput(t, ctx, sub, []byte("READY"))
	next, _ := m.Update(tea.PasteMsg{Content: "MUST-DROP"})
	m = next.(model)
	m.sendResize(4, 2)
	m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.cols == 4 && f.rows == 2 && f.problem == "" })
	next, _ = m.Update(tea.PasteMsg{Content: "OK\n"})
	m = next.(model)
	got := terminalNativeOutput(t, ctx, sub, []byte("OK\n"))
	if bytes.Contains(got, []byte("MUST-DROP")) {
		t.Fatal("unsupported geometry accepted input before explicit recovery")
	}
	for _, id := range []string{"a-unrelated", "z-selected"} {
		if s, ok := manager.Snapshot(id); !ok || s.Process != "running" {
			t.Fatalf("geometry recovery changed child: %+v", s)
		}
	}
}

func TestTerminalCanonicalChild(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "terminal-canonical-child" {
		return
	}
	stty := exec.Command(os.Args[len(os.Args)-1], "sane", "-echo")
	stty.Stdin = os.Stdin
	if err := stty.Run(); err != nil {
		t.Fatal(err)
	}
	events := make(chan os.Signal, 4)
	signal.Notify(events, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(events)
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		for {
			select {
			case <-stop:
				return
			case sig := <-events:
				fmt.Fprintf(os.Stdout, "SIGNAL %s\n", sig.String())
			}
		}
	}()
	defer func() { close(stop); <-joined }()
	fmt.Fprint(os.Stdout, "CANONICAL READY\n")
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			fmt.Fprintf(os.Stdout, "LINE %s", line)
		}
		if err == io.EOF {
			fmt.Fprint(os.Stdout, "CANONICAL EOF\n")
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTerminalNativeCanonicalSignalsEOF(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stty, err := exec.LookPath("stty")
	if err != nil {
		t.Fatal(err)
	}
	m, manager := terminalNativeAttachment(t, []string{exe, "-test.run=^TestTerminalCanonicalChild$", "--", "terminal-canonical-child", stty})
	m = terminalNativeTake(t, m)
	sub, err := manager.SubscribeTerminal(m.sessID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	terminalNativeOutput(t, ctx, sub, []byte("CANONICAL READY"))
	for _, tc := range []struct {
		key    tea.KeyPressMsg
		marker string
	}{{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "SIGNAL interrupt"}, {tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl}, "SIGNAL quit"}} {
		next, _ := m.Update(tc.key)
		m = next.(model)
		terminalNativeOutput(t, ctx, sub, []byte(tc.marker))
	}
	next, _ := m.Update(tea.PasteMsg{Content: "canonical text\n"})
	m = next.(model)
	terminalNativeOutput(t, ctx, sub, []byte("LINE canonical text"))
	next, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = next.(model)
	terminalNativeOutput(t, ctx, sub, []byte("CANONICAL EOF"))
	if s, ok := manager.Snapshot("a-unrelated"); !ok || s.Process != "running" {
		t.Fatalf("canonical EOF changed other child: %+v", s)
	}
}

func TestTerminalNativeOwnershipResizeDetach(t *testing.T) {
	m, manager := terminalNativeAttachment(t, nil)
	m = terminalNativeTake(t, m)
	m.sendResize(37, 9)
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.cols == 37 && f.rows == 9 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	second := client.New(m.api.Base, m.api.Token)
	stream, err := second.DialTerminalV1(ctx, m.sessID, "native-second")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	geometry, err := stream.Read(ctx)
	if err != nil || geometry.Cols != 37 || geometry.Rows != 9 {
		t.Fatalf("second native geometry: %+v %v", geometry, err)
	}
	foreign, err := second.ControlSession(ctx, m.sessID, client.ControlSessionRequest{Surface: "web", Client: "native-second"})
	if err != nil || foreign.OwnerClient != "native-second" {
		t.Fatalf("second take: %+v %v", foreign, err)
	}
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return !f.owned && f.owner == "native-second" })
	next, cmd := m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	m = next.(model)
	if cmd == nil || m.termMode {
		t.Fatal("detach did not return navigation")
	}
	cmd()
	for _, id := range []string{"a-unrelated", "z-selected"} {
		s, ok := manager.Snapshot(id)
		if !ok || s.Process != "running" {
			t.Fatalf("detach killed child: %+v", s)
		}
		if id == "z-selected" && s.OwnerClient != "native-second" {
			t.Fatalf("detach released another client: %+v", s)
		}
	}
}

func TestTerminalNativeExplicitStop(t *testing.T) {
	m, manager := terminalNativeAttachment(t, nil)
	m = terminalNativeTake(t, m)
	m.termMode = false
	next, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = next.(model)
	select {
	case <-m.link.done:
	case <-time.After(5 * time.Second):
		t.Fatal("explicit Stop did not join")
	}
	if s, ok := manager.Snapshot("z-selected"); ok && s.Process == "running" {
		t.Fatal("selected child survived Stop")
	}
	if s, ok := manager.Snapshot("a-unrelated"); !ok || s.Process != "running" || s.OwnerClient != "" {
		t.Fatalf("Stop changed unrelated child: %+v", s)
	}
}
