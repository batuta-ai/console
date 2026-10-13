package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/windows"
)

// This qualifies the real native pipe writer and its cancellation contract.
// ConPTY attachment and client lifetime have separate native process tests.
func task43NativeInputPipe(t *testing.T) (*Manager, *live, windows.Handle) {
	t.Helper()
	reader, writer, err := newWindowsConsolePipe(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(reader) })
	mode := uint32(windows.PIPE_NOWAIT)
	if err := windows.SetNamedPipeHandleState(writer, &mode, nil, nil); err != nil {
		windows.CloseHandle(writer)
		t.Fatal(err)
	}
	input := os.NewFile(uintptr(writer), "native input qualification")
	t.Cleanup(func() { input.Close() })
	l := &live{sess: domain.Session{ID: "pipe", Owner: "web", OwnerClient: "first"}, terminal: &windowsTerminal{input: input}}
	m := New(t.TempDir())
	m.live["pipe"] = l
	return m, l, reader
}

func TestTask43WindowsInputKernelSaturation(t *testing.T) {
	for _, action := range []string{"cancel", "transfer", "release"} {
		t.Run(action, func(t *testing.T) {
			m, l, reader := task43NativeInputPipe(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fill := bytes.Repeat([]byte{'x'}, 4096)
			accepted := 0
			for {
				var n uint32
				err := windows.WriteFile(windows.Handle(l.terminal.(*windowsTerminal).input.Fd()), fill, &n, nil)
				if err != nil {
					t.Fatal("independent native saturation", err)
				}
				accepted += int(n)
				if n == 0 {
					break
				}
				if accepted > 1<<20 || ctx.Err() != nil {
					t.Fatal("native byte pipe never reached observed zero-byte write", accepted, ctx.Err())
				}
			}
			if accepted == 0 {
				t.Fatal("native byte pipe accepted no initial bytes")
			}
			t.Logf("native kernel saturation: successful zero-byte write after %d bytes", accepted)
			input, err := m.PrepareTerminalInput("pipe", "first", []byte("STALE"))
			if err != nil {
				t.Fatal(err)
			}
			writeCtx, cancelWrite := context.WithCancel(ctx)
			defer cancelWrite()
			result := make(chan error, 1)
			go func() { result <- input.Write(writeCtx) }()
			stack := make([]byte, 1<<20)
			for !bytes.Contains(stack[:runtime.Stack(stack, true)], []byte("session.waitTerminalWritable(")) {
				select {
				case err := <-result:
					t.Fatal("writer did not wait on a saturated native pipe", err)
				case <-ctx.Done():
					t.Fatal("native writer readiness wait not observed", ctx.Err())
				default:
					runtime.Gosched()
				}
			}
			responsive := make(chan error, 1)
			go func() {
				if _, ok := m.Snapshot("pipe"); !ok {
					responsive <- os.ErrNotExist
					return
				}
				switch action {
				case "cancel":
					cancelWrite()
					responsive <- nil
				case "transfer":
					_, err := m.Take("pipe", "tui", "second")
					responsive <- err
				case "release":
					_, err := m.Take("pipe", "none", "first")
					responsive <- err
				}
			}()
			select {
			case err := <-responsive:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("snapshot/control blocked behind native input", ctx.Err())
			}
			want := os.ErrPermission
			if action == "cancel" {
				want = context.Canceled
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatal("native blocked writer returned wrong cancellation", err)
				}
			case <-ctx.Done():
				t.Fatal("native blocked writer did not cancel", ctx.Err())
			}
			queued := make([]byte, accepted)
			var n uint32
			if err := windows.ReadFile(reader, queued, &n, nil); err != nil || int(n) != accepted || !bytes.Equal(queued, bytes.Repeat([]byte{'x'}, accepted)) {
				t.Fatal("revoked bytes reached the saturated native pipe", n, err)
			}
			if _, err := m.Take("pipe", "web", "first"); err != nil {
				t.Fatal(err)
			}
			literal := []byte{'A', 0, '\r', '\n', 0x1b, 'Z'}
			if err := m.InputContext(ctx, "pipe", "first", literal); err != nil {
				t.Fatal(err)
			}
			actual := make([]byte, len(literal))
			if err := windows.ReadFile(reader, actual, &n, nil); err != nil || int(n) != len(literal) || !bytes.Equal(actual, literal) {
				t.Fatal("native writer changed literal bytes or retained cancelled input", actual, n, err)
			}
			l.mu.Lock()
			pending, gate := l.pendingInput, len(l.inputGate)
			l.mu.Unlock()
			if pending != 0 || gate != 0 {
				t.Fatal("native cancellation retained input admission", pending, gate)
			}
		})
	}
}

func TestTask43WindowsInputKernelClosedWriter(t *testing.T) {
	m, l, _ := task43NativeInputPipe(t)
	input, err := m.PrepareTerminalInput("pipe", "first", []byte("must-fail"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.terminal.(*windowsTerminal).input.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := input.Write(ctx); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed native input was successful or retried until timeout", err)
	}
}

func TestTask43WindowsInputKernelDisconnect(t *testing.T) {
	m, _, reader := task43NativeInputPipe(t)
	if err := windows.DisconnectNamedPipe(reader); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := m.InputContext(ctx, "pipe", "first", []byte("must-fail"))
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		t.Fatal("disconnected native pipe did not report its kernel failure immediately", err)
	}
}
