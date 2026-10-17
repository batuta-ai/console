package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

type windowsCommandStreams struct {
	input    *os.File
	output   *os.File
	stderr   *os.File
	children []windows.Handle
}

func (s *windowsCommandStreams) closeChildren() error {
	var err error
	for i, handle := range s.children {
		if handle != 0 {
			err = errors.Join(err, windows.CloseHandle(handle))
			s.children[i] = 0
		}
	}
	return err
}

func (s *windowsCommandStreams) Close() error {
	err := s.closeChildren()
	for _, file := range []*os.File{s.input, s.output, s.stderr} {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				err = errors.Join(err, closeErr)
			}
		}
	}
	return err
}

func newWindowsCommandStreams() (*windowsCommandStreams, error) {
	s := &windowsCommandStreams{}
	fail := func(err error) (*windowsCommandStreams, error) { return nil, errors.Join(err, s.Close()) }
	for i := 0; i < 3; i++ {
		reader, writer, err := newWindowsConsolePipe(i != 0)
		if err != nil {
			return fail(err)
		}
		host, child := reader, writer
		if i == 0 {
			host, child = writer, reader
		}
		var inherited windows.Handle
		err = windows.DuplicateHandle(windows.CurrentProcess(), child, windows.CurrentProcess(), &inherited, 0, true, windows.DUPLICATE_SAME_ACCESS)
		closeErr := windows.CloseHandle(child)
		if inherited != 0 {
			s.children = append(s.children, inherited)
		}
		if err != nil || closeErr != nil {
			return fail(errors.Join(err, closeErr, windows.CloseHandle(host)))
		}
		file := os.NewFile(uintptr(host), "native command stream")
		switch i {
		case 0:
			s.input = file
		case 1:
			s.output = file
		case 2:
			s.stderr = file
		}
	}
	return s, nil
}

// Console control has a separate private ConPTY channel. Only the three
// explicit standard streams are inherited by the freshly born command.
func (m *Manager) runWindowsConsoleCommand(ctx context.Context, cmd *exec.Cmd) (exitCode int, runErr error) {
	exitCode = -1
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	streams, err := newWindowsCommandStreams()
	if err != nil {
		return exitCode, err
	}
	var console *windowsTerminal
	var owned *ownedProcess
	registered := false
	defer func() {
		cleanup := streams.Close()
		consoleDecisionEvent(owned, "deferred-streams-close", cleanup)
		if console != nil {
			cleanup = errors.Join(cleanup, consoleDecisionResult(owned, "console-close", console.Close()))
		}
		runErr = errors.Join(runErr, cleanup)
		if registered {
			m.mu.Lock()
			delete(m.probes, owned)
			m.mu.Unlock()
			if cleanup != nil || errors.Is(runErr, ErrNativeCleanupUnresolved) {
				m.CloseAdmission()
				unresolved := errors.Join(ErrNativeCleanupUnresolved, runErr)
				m.recordCleanup(unresolved, unresolved)
			}
		}
		runErr = errors.Join(runErr, context.Cause(ctx))
		consoleDecisionReturn(owned, ctx, exitCode, runErr)
	}()
	terminal, process, err := startWindowsConsole(cmd, 80, 24, streams.children)
	if err != nil {
		return exitCode, err
	}
	console, owned = terminal.(*windowsTerminal), process
	beginConsoleDecision(owned)
	if err := streams.closeChildren(); err != nil {
		consoleDecisionEvent(owned, "child-close-error", err)
		return exitCode, errors.Join(err, owned.retire(nil))
	}
	ioDone := make(chan error, 3)
	go func() {
		_, err := io.Copy(streams.input, cmd.Stdin)
		consoleDecisionEvent(owned, "stdin-copy-raw", err)
		// Like exec.Cmd's finite stdin, an exited command may close the input
		// without consuming the rest; output/observer failures remain errors.
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) {
			err = nil
		}
		ioDone <- errors.Join(err, consoleDecisionResult(owned, "stdin-close", streams.input.Close()))
	}()
	copyOutput := func(destination io.Writer, source io.Reader) {
		_, err := io.Copy(destination, source)
		if source == streams.stderr {
			consoleDecisionEvent(owned, "stderr-copy", err)
		} else {
			consoleDecisionEvent(owned, "stdout-copy", err)
		}
		if err != nil {
			cancel(err)
		}
		ioDone <- err
	}
	go copyOutput(cmd.Stdout, streams.output)
	go copyOutput(cmd.Stderr, streams.stderr)
	owned.mu.Lock()
	owned.cancel = func() { cancel(context.Canceled) }
	owned.mu.Unlock()
	m.mu.Lock()
	m.probes[owned] = struct{}{}
	closing := m.closing
	m.mu.Unlock()
	registered = true
	done := make(chan error, 1)
	go func() {
		cleanup, wait := waitOwnedOutcome(cmd, owned)
		consoleDecisionOutcome(owned, cleanup, wait)
		if cleanup != nil {
			cleanup = errors.Join(ErrNativeCleanupUnresolved, cleanup)
		}
		done <- errors.Join(cleanup, wait)
	}()
	if closing {
		cancel(context.Canceled)
	}
	select {
	case err = <-done:
		consoleDecisionEvent(owned, "outer-done", err)
	case <-ctx.Done():
		consoleDecisionCanceled(owned, ctx)
		// Win32 input records deliver CTRL+BREAK even when the client inherited
		// CTRL+C-ignore. Control stays on this freshly owned pseudoconsole.
		interrupt := []byte("\x1b[3;0;0;1;8;1_\x1b[3;0;0;0;8;1_")
		written, signalErr := writeTerminalChunk(console, interrupt)
		consoleDecisionInjection(owned, written, len(interrupt), signalErr)
		if written != len(interrupt) {
			signalErr = errors.Join(signalErr, io.ErrShortWrite)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case err = <-done:
			consoleDecisionEvent(owned, "grace-done", err)
			err = errors.Join(signalErr, err)
		case <-timer.C:
			consoleDecisionEvent(owned, "grace-timer", nil)
			err = errors.Join(ErrNativeCleanupUnresolved, publication.ErrReviewCleanupUnresolved, signalErr, owned.stop(), <-done)
		}
		timer.Stop()
	}
	runErr = errors.Join(runErr, err)
	// Join actual pipe EOF after the whole Job is empty. An externally retained
	// descriptor or failed observer cannot become a successful engine result.
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	for joined := 0; joined < 3; joined++ {
		select {
		case err := <-ioDone:
			consoleDecisionEvent(owned, "pipe-joined", err)
			runErr = errors.Join(runErr, err)
		case <-timer.C:
			consoleDecisionEvent(owned, "pipe-timer", nil)
			runErr = errors.Join(runErr, exec.ErrWaitDelay, consoleDecisionResult(owned, "pipe-timeout-close", streams.Close()))
			for ; joined < 3; joined++ {
				runErr = errors.Join(runErr, <-ioDone)
			}
		}
	}
	owned.mu.Lock()
	forced, code := owned.forced, owned.exitCode
	consoleDecisionFinal(owned, ctx, forced, code)
	owned.mu.Unlock()
	if forced || ctx.Err() != nil && code != 0 {
		consoleDecisionEvent(owned, "final-promoted", nil)
		runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, publication.ErrReviewCleanupUnresolved)
	}
	if runErr == nil && ctx.Err() == nil {
		exitCode = int(int32(code))
	}
	return exitCode, runErr
}
