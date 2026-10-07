//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/unix"
)

// The child receives commands on a FIFO independent of the PTY input queue.
// It can prove output progress while deliberately refusing to read stdin.
func TestTerminalNonReadingChild(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "terminal-child" {
		return
	}
	control, err := os.Open(os.Args[len(os.Args)-1])
	if err != nil {
		panic(err)
	}
	stty := exec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	if err := stty.Run(); err != nil {
		panic(err)
	}
	fmt.Print("READY")
	var command [1]byte
	for {
		if _, err := io.ReadFull(control, command[:]); err != nil {
			panic(err)
		}
		switch command[0] {
		case 'o':
			fmt.Print("PULSE")
		case 'r':
			// Echo one byte at a time until the fresh owner's terminator. Anything
			// accepted before revocation is allowed, but must precede that terminator.
			var b [1]byte
			for {
				if _, err := io.ReadFull(os.Stdin, b[:]); err != nil {
					panic(err)
				}
				if _, err := os.Stdout.Write(b[:]); err != nil {
					panic(err)
				}
				if b[0] == '!' {
					break
				}
			}
		case 'q':
			os.Exit(0)
		}
	}
}

func terminalNativeChild(t *testing.T, m *Manager, id string) (*Pending, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	control, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.StartPending(domain.Session{ID: id, Cols: 80, Rows: 24, Argv: []string{exe, "-test.run=^TestTerminalNonReadingChild$", "--", "terminal-child", path}}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return p, control
}

func terminalReadBytes(t *testing.T, ctx context.Context, sub *TerminalSubscription, end []byte) []byte {
	t.Helper()
	var got []byte
	for !bytes.HasSuffix(got, end) {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, event.Data...)
	}
	return got
}

func TestTerminalInputNonReadingCancelAndRevoke(t *testing.T) {
	for _, action := range []string{"cancel", "transfer", "release", "stop"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			m := newTestManager(t)
			p, control := terminalNativeChild(t, m, "blocked")
			sub, err := m.SubscribeTerminal("blocked")
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			if got := terminalReadBytes(t, ctx, sub, []byte("READY")); string(got) != "READY" {
				t.Fatalf("ready %q", got)
			}
			if _, err = m.Take("blocked", "tui", "owner"); err != nil {
				t.Fatal(err)
			}
			other, otherControl := terminalNativeChild(t, m, "other")
			otherSub, err := m.SubscribeTerminal("other")
			if err != nil {
				t.Fatal(err)
			}
			defer otherSub.Close()
			terminalReadBytes(t, ctx, otherSub, []byte("READY"))
			l, _ := m.Live("blocked")
			// Fill the real master until the kernel returns EAGAIN. This is a
			// test-side observation, independent of the production readiness helper.
			raw, err := l.pty.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			accepted := 0
			fill := bytes.Repeat([]byte("x"), 4096)
			for {
				var n int
				var writeErr error
				if err := raw.Control(func(fd uintptr) { n, writeErr = unix.Write(int(fd), fill) }); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					accepted += n
				}
				if errors.Is(writeErr, unix.EAGAIN) {
					break
				}
				if writeErr != nil {
					t.Fatal(writeErr)
				}
				if ctx.Err() != nil || accepted > 1<<20 {
					t.Fatalf("no observed saturation: %d %v", accepted, ctx.Err())
				}
			}
			t.Logf("kernel input saturation: EAGAIN after %d bytes", accepted)
			payload := append(bytes.Repeat([]byte("x"), 256<<10), []byte("STALE-BLOCKED")...)
			input, err := m.PrepareTerminalInput("blocked", "owner", payload)
			if err != nil {
				t.Fatal(err)
			}
			writeCtx, stopWrite := context.WithCancel(ctx)
			defer stopWrite()
			result := make(chan error, 1)
			go func() { result <- input.Write(writeCtx) }()
			// Observe that the production writer acquired the serialization gate;
			// combined with kernel saturation and no child reads, it cannot finish.
			for {
				l.mu.Lock()
				active := len(l.inputGate) == 1
				l.mu.Unlock()
				if active {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("writer completed before revocation: %v", err)
				default:
				}
				if ctx.Err() != nil {
					t.Fatal("writer never acquired gate")
				}
				runtime.Gosched()
			}
			// A full kernel plus an occupied gate alone could still precede the
			// first write attempt. Observe the production goroutine in its
			// readiness wait before revoking it; no production hook is used.
			stacks := make([]byte, 1<<20)
			for {
				n := runtime.Stack(stacks, true)
				if bytes.Contains(stacks[:n], []byte("session.waitTerminalWritable(")) {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("writer completed before readiness wait: %v", err)
				default:
				}
				if ctx.Err() != nil {
					t.Fatal("production writer never reached readiness wait")
				}
				runtime.Gosched()
			}
			t.Log("observed production writer waiting for PTY readiness")
			stale, err := m.PrepareTerminalInput("blocked", "owner", []byte("STALE-QUEUED"))
			if err != nil {
				t.Fatal(err)
			}
			defer stale.Close()
			// These operations must complete while the writer remains blocked.
			responsive := make(chan error, 1)
			go func() {
				if _, ok := m.Snapshot("blocked"); !ok {
					responsive <- fmt.Errorf("missing snapshot")
					return
				}
				view, err := m.SubscribeTerminal("blocked")
				if err != nil {
					responsive <- err
					return
				}
				view.Close()
				_, err = m.Take("other", "web", "other-owner")
				responsive <- err
			}()
			select {
			case err = <-responsive:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("session operations blocked behind input")
			}
			if _, err = control.Write([]byte("o")); err != nil {
				t.Fatal(err)
			}
			if got := terminalReadBytes(t, ctx, sub, []byte("PULSE")); string(got) != "PULSE" {
				t.Fatalf("output while saturated: %q", got)
			}
			if _, err = otherControl.Write([]byte("or")); err != nil {
				t.Fatal(err)
			}
			if err = m.InputContext(ctx, "other", "other-owner", []byte("OTHER!")); err != nil {
				t.Fatal(err)
			}
			if got := terminalReadBytes(t, ctx, otherSub, []byte("OTHER!")); string(got) != "PULSEOTHER!" {
				t.Fatalf("other session: %q", got)
			}
			revoked := make(chan error, 1)
			go func() {
				var err error
				switch action {
				case "cancel":
					stopWrite()
				case "transfer":
					_, err = m.Take("blocked", "web", "second")
				case "release":
					_, err = m.Take("blocked", "none", "owner")
				case "stop":
					_, err = m.Stop("blocked")
				}
				revoked <- err
			}()
			select {
			case err = <-revoked:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("revocation blocked")
			}
			select {
			case err = <-result:
				want := os.ErrPermission
				if action == "cancel" {
					want = context.Canceled
				} else if action == "stop" {
					want = os.ErrClosed
				}
				if !errors.Is(err, want) {
					t.Fatalf("joined write: %v, want %v", err, want)
				}
			case <-ctx.Done():
				t.Fatal("input did not join")
			}
			if action == "stop" {
				if err = stale.Write(ctx); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("stopped stale input: %v", err)
				}
				snap := p.Snapshot()
				observed, inspectErr := InspectProcess(snap.PID)
				if inspectErr != nil || !observed.Missing || snap.Process != "exited" {
					t.Fatalf("owned child survived stop: %+v %+v %v", snap, observed, inspectErr)
				}
				if _, err = otherControl.Write([]byte("o")); err != nil {
					t.Fatal(err)
				}
				if got := terminalReadBytes(t, ctx, otherSub, []byte("PULSE")); string(got) != "PULSE" {
					t.Fatalf("other child after stop: %q", got)
				}
				if other.Snapshot().Process != "running" {
					t.Fatal("other child lost after stop")
				}
				l.mu.Lock()
				pending := l.pendingInput
				l.mu.Unlock()
				if pending != 0 {
					t.Fatalf("stopped input reservation leaked: %d", pending)
				}
				return
			}
			if action == "cancel" {
				if _, err = m.Take("blocked", "web", "second"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = m.Take("blocked", "tui", "owner"); err != nil {
				t.Fatal(err)
			}
			if err = stale.Write(ctx); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("stale reservation: %v", err)
			}
			if _, err = control.Write([]byte("r")); err != nil {
				t.Fatal(err)
			}
			fresh := []byte("FRESH!")
			if err = m.InputContext(ctx, "blocked", "owner", fresh); err != nil {
				t.Fatal(err)
			}
			got := terminalReadBytes(t, ctx, sub, fresh)
			prefix := got[:len(got)-len(fresh)]
			if len(prefix) < accepted || !bytes.Equal(prefix, bytes.Repeat([]byte("x"), len(prefix))) {
				t.Fatalf("stale or reordered child bytes: accepted=%d observed=%d tail=%q", accepted, len(prefix), got[max(0, len(got)-80):])
			}
			l.mu.Lock()
			pending := l.pendingInput
			l.mu.Unlock()
			if pending != 0 {
				t.Fatalf("pending input leaked: %d", pending)
			}
			for _, child := range []*Pending{p, other} {
				snap := child.Snapshot()
				observed, err := InspectProcess(snap.PID)
				if err != nil || observed.Missing || snap.Process != "running" {
					t.Fatalf("child lost: %+v %+v %v", snap, observed, err)
				}
			}
		})
	}
}

func TestTerminalInputExactRawAndCanonical(t *testing.T) {
	for _, mode := range []string{"raw", "canonical"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			m := newTestManager(t)
			script := `stty raw -echo; printf READY; cat`
			input := bytes.Repeat([]byte{0, 1, 3, 4, 26, 28, 27, '[', 'A', 0xc3, 0xa9}, 12000)
			want := input
			if mode == "canonical" {
				script = `stty -echo; printf READY; while IFS= read -r line; do printf 'ACK:%s\n' "$line"; done`
				input = []byte("first line\nsecond \\ line\n")
				want = []byte("ACK:first line\r\nACK:second \\ line\r\n")
			}
			_, err := m.StartPending(domain.Session{ID: mode, Argv: []string{"/bin/sh", "-c", script}}, nil, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := m.SubscribeTerminal(mode)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			if got := terminalReadBytes(t, ctx, sub, []byte("READY")); string(got) != "READY" {
				t.Fatalf("ready %q", got)
			}
			if _, err = m.Take(mode, "tui", "owner"); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { result <- m.InputContext(ctx, mode, "owner", input) }()
			var got []byte
			for len(got) < len(want) {
				e, err := sub.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, e.Data...)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s child bytes differ: %d/%d", mode, len(got), len(want))
			}
			select {
			case err = <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("writer did not join")
			}
		})
	}
}

func TestTerminalNativeResizeRangeAndTruth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := newTestManager(t)
	_, control := terminalNativeChild(t, m, "geometry")
	sub, err := m.SubscribeTerminal("geometry")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	terminalReadBytes(t, ctx, sub, []byte("READY"))
	if _, err = m.Take("geometry", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	l, _ := m.Live("geometry")
	for _, size := range [][2]int{{1, 1}, {501, 201}, {65535, 65535}, {97, 31}} {
		if err = m.Resize("geometry", "owner", size[0], size[1]); err != nil {
			t.Fatal(err)
		}
		event, err := sub.Next(ctx)
		if err != nil || event.Data != nil || event.Cols != size[0] || event.Rows != size[1] {
			t.Fatalf("resize event %+v %v", event, err)
		}
		raw, err := l.pty.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var ws *unix.Winsize
		var ioctlErr error
		if err = raw.Control(func(fd uintptr) { ws, ioctlErr = unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ) }); err != nil {
			t.Fatal(err)
		}
		if ioctlErr != nil || int(ws.Col) != size[0] || int(ws.Row) != size[1] {
			t.Fatalf("kernel geometry %+v %v", ws, ioctlErr)
		}
	}
	for _, tc := range []struct {
		owner      string
		cols, rows int
	}{{"owner", 0, 24}, {"owner", 80, 0}, {"owner", -1, 24}, {"owner", 65536, 24}, {"owner", 80, 65536}, {"foreign", 123, 45}} {
		if err = m.Resize("geometry", tc.owner, tc.cols, tc.rows); err == nil {
			t.Fatalf("invalid resize accepted %+v", tc)
		}
		snap, _ := m.Snapshot("geometry")
		if snap.Cols != 97 || snap.Rows != 31 || snap.OwnerClient != "owner" {
			t.Fatalf("failed resize changed truth %+v", snap)
		}
		if _, err = control.Write([]byte("o")); err != nil {
			t.Fatal(err)
		}
		var got []byte
		for len(got) < len("PULSE") {
			e, err := sub.Next(ctx)
			if err != nil || e.Data == nil {
				t.Fatalf("failed resize emitted geometry %+v %v", e, err)
			}
			got = append(got, e.Data...)
		}
		if string(got) != "PULSE" {
			t.Fatalf("output after failed resize %q", got)
		}
	}
	sub.Close()
	l.mu.Lock()
	remaining := len(l.terminalSubs)
	l.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("subscriber leaked: %d", remaining)
	}
}

func TestTerminalInputClosedAfterStopOrNaturalExit(t *testing.T) {
	for _, mode := range []string{"stop", "natural-exit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m := newTestManager(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			stopped := make(chan error, 1)
			defer func() {
				close(release)
				if mode == "stop" {
					select {
					case err := <-stopped:
						if err != nil {
							t.Error(err)
						}
					case <-ctx.Done():
						t.Error("Stop did not join")
					}
				}
			}()
			m.OnExit = func(domain.Session) { close(entered); <-release }
			_, control := terminalNativeChild(t, m, "closing")
			sub, err := m.SubscribeTerminal("closing")
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			terminalReadBytes(t, ctx, sub, []byte("READY"))
			if _, err = m.Take("closing", "tui", "owner"); err != nil {
				t.Fatal(err)
			}
			reserved, err := m.PrepareTerminalInput("closing", "owner", []byte("UNSENT"))
			if err != nil {
				t.Fatal(err)
			}
			defer reserved.Close()
			if mode == "stop" {
				go func() { _, err := m.Stop("closing"); stopped <- err }()
			} else if _, err = control.Write([]byte("q")); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("exit callback not entered")
			}
			if err = reserved.Write(ctx); !errors.Is(err, os.ErrClosed) {
				t.Errorf("reserved input after %s: %v, want closed", mode, err)
			}
			next, err := m.PrepareTerminalInput("closing", "owner", []byte("NEW"))
			if next != nil {
				next.Close()
			}
			if next != nil || !errors.Is(err, os.ErrClosed) {
				t.Fatalf("new input while closed: %v %v", next, err)
			}
		})
	}
}
