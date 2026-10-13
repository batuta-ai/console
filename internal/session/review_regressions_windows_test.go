package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestWindowsTerminalClosedReaderReturnsKernelFailure(t *testing.T) {
	reader, writer, err := newWindowsConsolePipe(false)
	if err != nil {
		t.Fatal(err)
	}
	mode := uint32(windows.PIPE_NOWAIT)
	if err := windows.SetNamedPipeHandleState(writer, &mode, nil, nil); err != nil {
		t.Fatal(err)
	}
	input := os.NewFile(uintptr(writer), "closed reader regression")
	t.Cleanup(func() {
		if err := input.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := windows.CloseHandle(reader); err != nil {
		t.Fatal(err)
	}
	l := &live{sess: domain.Session{ID: "pipe", Owner: "web", OwnerClient: "first"}, terminal: &windowsTerminal{input: input}}
	m := New(t.TempDir())
	m.live["pipe"] = l
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = m.InputContext(ctx, "pipe", "first", []byte("literal"))
	if !errors.Is(err, windows.ERROR_NO_DATA) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatal("closed real pipe lost its immediate kernel cause", err)
	}
	l.mu.Lock()
	pending, gate := l.pendingInput, len(l.inputGate)
	l.mu.Unlock()
	if pending != 0 || gate != 0 {
		t.Fatal("closed pipe retained input admission", pending, gate)
	}
}

func TestWindowsExchangeProtocolTimeoutKeepsCleanManager(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	foreign := task43ForeignSibling(t)
	var witnesses []windows.Handle
	result, err := runner.Exchange(ctx, task43ExchangeCommand(t, "hold", "owned"), func(_ context.Context, output io.Reader, _ io.WriteCloser) error {
		birth, err := task43ExchangeReceiveBirth(output)
		if err == nil {
			for _, identity := range []string{birth.Root, birth.Child} {
				witnesses = append(witnesses, task43ObserveLifetimeChild(t, identity))
			}
		}
		return err
	})
	if result.ExitCode != -1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("protocol timeout was not reported", result.ExitCode, err)
	}
	for _, process := range witnesses {
		if status, err := windows.WaitForSingleObject(process, 0); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal("protocol timeout left owned process alive", status, err)
		}
	}
	task43SiblingStillRunning(t, foreign)
	if err := runner.Manager.Shutdown(ctx); err != nil {
		t.Fatal("successful drain poisoned manager", err)
	}
}

func TestWindowsNativeConsolePreservesLookupFailure(t *testing.T) {
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	// Go records the lookup failure only for a bare executable name.
	t.Setenv("PATH", home)
	command := exec.Command("missing-owned-executable")
	command.Env = env
	if command.Err == nil {
		t.Fatal("real lookup did not fail")
	}
	terminal, owned, err := startWindowsConsole(command, 80, 24, nil)
	if terminal != nil {
		terminal.Close()
	}
	if owned != nil {
		owned.retire(nil)
	}
	if err == nil || !errors.Is(err, command.Err) {
		t.Fatal("native console bypassed Go lookup refusal", err, command.Err)
	}
}

func TestWindowsGracefulObserverFailureCancelsOwnedCommand(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	runner.Graceful = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := write.Close(); err != nil {
			t.Error(err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := publication.Command{Executable: exe, Args: []string{"-test.run=^TestTask43WindowsGracefulEngineFixture$", "--", "task43-graceful-engine", "cooperate"}, Observer: write}
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, command); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, windows.ERROR_NO_DATA) {
			t.Fatal("real observer pipe error was lost", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("graceful command ignored real output observer failure")
	}
}

func TestWindowsNativeConsolePreservesCurrentDirectoryRefusal(t *testing.T) {
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	t.Chdir(home)
	t.Setenv("PATH", filepath.Join(home, "absent-path"))
	t.Setenv("GODEBUG", "execerrdot=1")
	if err := os.WriteFile("current-directory-probe.exe", []byte("owned lookup fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("current-directory-probe.exe")
	command.Env = env
	if !errors.Is(command.Err, exec.ErrDot) {
		t.Fatal("real current-directory lookup did not refuse", command.Err)
	}
	terminal, owned, err := startWindowsConsole(command, 80, 24, nil)
	if terminal != nil {
		terminal.Close()
	}
	if owned != nil {
		owned.retire(nil)
	}
	if terminal != nil || owned != nil || !errors.Is(err, exec.ErrDot) {
		t.Fatal("native console bypassed current-directory refusal", err)
	}
}
