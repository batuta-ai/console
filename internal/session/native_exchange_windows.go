package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

type nativeExchangeFile struct {
	*os.File
	once sync.Once
	err  error
}

func (f *nativeExchangeFile) Close() error {
	f.once.Do(func() { f.err = f.File.Close() })
	return f.err
}

type nativeExchangePipes struct {
	inputRead, inputWrite, outputRead, outputWrite *nativeExchangeFile
	stderrRead, stderrWrite                        *nativeExchangeFile
}

func newNativeExchangePipe() (*os.File, *os.File, error) {
	var read, write windows.Handle
	// nil security attributes prevent inheritance at creation; os.Pipe marks
	// both handles inheritable before Go's restricted child duplication.
	if err := windows.CreatePipe(&read, &write, nil, 0); err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(read), "|0"), os.NewFile(uintptr(write), "|1"), nil
}

func newNativeExchangePipes() (nativeExchangePipes, error) {
	inputRead, inputWrite, err := newNativeExchangePipe()
	if err != nil {
		return nativeExchangePipes{}, err
	}
	outputRead, outputWrite, err := newNativeExchangePipe()
	if err != nil {
		return nativeExchangePipes{}, errors.Join(err, inputRead.Close(), inputWrite.Close())
	}
	stderrRead, stderrWrite, err := newNativeExchangePipe()
	if err != nil {
		return nativeExchangePipes{}, errors.Join(err, inputRead.Close(), inputWrite.Close(), outputRead.Close(), outputWrite.Close())
	}
	return nativeExchangePipes{
		inputRead: &nativeExchangeFile{File: inputRead}, inputWrite: &nativeExchangeFile{File: inputWrite},
		outputRead: &nativeExchangeFile{File: outputRead}, outputWrite: &nativeExchangeFile{File: outputWrite},
		stderrRead: &nativeExchangeFile{File: stderrRead}, stderrWrite: &nativeExchangeFile{File: stderrWrite},
	}, nil
}

func (p nativeExchangePipes) Close() error {
	return errors.Join(p.inputRead.Close(), p.inputWrite.Close(), p.outputRead.Close(), p.outputWrite.Close(), p.stderrRead.Close(), p.stderrWrite.Close())
}

func newNativeProtocolCompletion() (func(), <-chan time.Time) {
	protocolClosed := make(chan time.Time, 1)
	var closeProtocol sync.Once
	return func() {
		closeProtocol.Do(func() { protocolClosed <- time.Now().Add(2 * time.Second) })
	}, protocolClosed
}

func (r NativeRunner) runNativeExchange(ctx context.Context, command publication.Command, handler func(context.Context, io.Reader, io.WriteCloser) error, finalizing func(NativeExchangeFinalization, io.Reader, io.WriteCloser) error) (result publication.CommandResult, runErr error) {
	result.ExitCode = -1
	if r.Manager == nil || (handler == nil && finalizing == nil) || !filepath.IsAbs(command.Executable) || r.Graceful || len(command.Stdin) != 0 || command.Observer != nil || command.StdoutLimit != 0 || command.StderrLimit < 0 || command.StderrLimit > 16<<20 {
		return result, errors.New("invalid native exchange command or manager")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	m := r.Manager
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return result, ErrProbeClosing
	}
	m.active.Add(1)
	m.mu.Unlock()
	defer m.active.Done()
	explicit, privateHome := r.Environment, ""
	if accounts.ExplicitHome(explicit) == "" {
		env, home, err := accounts.SessionEnvironment()
		if err != nil {
			return result, err
		}
		explicit, privateHome = append(append([]string{}, explicit...), env...), home
	}
	defer func() {
		if privateHome != "" {
			if err := os.RemoveAll(privateHome); err != nil {
				result.ExitCode = -1
				runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, err)
				m.CloseAdmission()
				m.recordCleanup(err, err)
			}
		}
	}()
	env, err := NativeEnvironment(explicit, command.Environment)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	pipes, err := newNativeExchangePipes()
	if err != nil {
		return result, err
	}
	defer func() {
		if err := pipes.Close(); err != nil {
			result.ExitCode = -1
			runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, err)
			m.CloseAdmission()
			m.recordCleanup(err, err)
		}
	}()
	limit := command.StderrLimit
	if limit == 0 {
		limit = 64 << 10
	}
	stderr := &nativeOutput{limit: limit}
	cmd := exec.Command(command.Executable, command.Args...)
	cmd.Env, cmd.Dir = env, command.Directory
	// Original standard handles are duplicated into the suspended Job anchor.
	// The parent owns all three drains independently of Cmd.Wait, which can
	// hide copying errors when the child also exits unsuccessfully.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pipes.inputRead.File, pipes.outputWrite.File, pipes.stderrWrite.File
	var stderrWriter io.Writer = stderr
	if command.StderrObserver != nil {
		stderrWriter = io.MultiWriter(stderr, command.StderrObserver)
	}
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		return result, err
	}
	if err := errors.Join(pipes.inputRead.Close(), pipes.outputWrite.Close(), pipes.stderrWrite.Close()); err != nil {
		cleanup := errors.Join(owned.stop(), waitOwned(cmd, owned))
		m.CloseAdmission()
		m.recordCleanup(errors.Join(err, cleanup), cleanup)
		return result, errors.Join(err, cleanup)
	}
	stopContext, requestStop := context.WithCancel(ctx)
	defer requestStop()
	callbackContext, cancelCallback := context.WithCancel(stopContext)
	defer cancelCallback()
	owned.mu.Lock()
	owned.cancel = requestStop
	owned.mu.Unlock()
	m.mu.Lock()
	m.probes[owned] = struct{}{}
	closing := m.closing
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.probes, owned)
		m.mu.Unlock()
	}()
	if closing {
		requestStop()
	}
	callbackDone := make(chan error, 1)
	cleanupContext, cancelCleanup := context.WithCancel(context.WithoutCancel(stopContext))
	defer cancelCleanup()
	protocolDone, protocolClosed := newNativeProtocolCompletion()
	go func() {
		if finalizing != nil {
			callbackDone <- finalizing(NativeExchangeFinalization{
				Control: stopContext, Cleanup: cleanupContext,
				ProtocolDone: protocolDone,
			}, pipes.outputRead, pipes.inputWrite)
		} else {
			callbackDone <- handler(callbackContext, pipes.outputRead, pipes.inputWrite)
		}
	}()
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stderrWriter, pipes.stderrRead)
		stderrDone <- errors.Join(err, pipes.stderrRead.Close())
	}()
	processDone := make(chan error, 1)
	go func() {
		ownership, wait := waitOwnedOutcome(cmd, owned)
		cleanup := errors.Join(ownership, wait)
		if cleanup != nil {
			cleanup = errors.Join(ErrNativeCleanupUnresolved, cleanup)
			m.CloseAdmission()
			m.recordCleanup(cleanup, cleanup)
		}
		processDone <- cleanup
	}()
	if finalizing != nil {
		runErr = m.waitNativeFinalization(stopContext, cancelCleanup, protocolClosed, pipes, owned, callbackDone, processDone, stderrDone)
		result.Stderr, result.StderrTruncated = append([]byte(nil), stderr.data...), stderr.truncated
		owned.mu.Lock()
		forced := owned.forced
		owned.mu.Unlock()
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			if code > 0 && !forced {
				runErr = errors.Join(runErr, &NativeExchangeExitError{Code: code})
			}
			if runErr == nil {
				result.ExitCode = code
			}
		}
		return result, runErr
	}
	return m.waitNativeLegacyExchange(stopContext, cancelCallback, pipes, owned, callbackDone, processDone, stderrDone, cmd, stderr, result)
}

func (m *Manager) waitNativeLegacyExchange(
	stopContext context.Context,
	cancelCallback context.CancelFunc,
	pipes nativeExchangePipes,
	owned *ownedProcess,
	callbackDone, processDone, stderrDone <-chan error,
	cmd *exec.Cmd,
	stderr *nativeOutput,
	result publication.CommandResult,
) (publication.CommandResult, error) {
	var runErr error
	var callbackErr, processErr, stderrErr, stopErr, protocolErr error
	stopOwned := func() {
		if err := owned.stop(); err != nil {
			cleanup := errors.Join(ErrNativeCleanupUnresolved, err)
			stopErr = errors.Join(stopErr, cleanup)
			m.CloseAdmission()
			m.recordCleanup(cleanup, cleanup)
		}
	}
	stopRequested := stopContext.Done()
	var grace *time.Timer
	var expired <-chan time.Time
	startGrace := func() {
		if grace == nil {
			grace = time.NewTimer(2 * time.Second)
			expired = grace.C
		}
	}
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()
	for callbackDone != nil || processDone != nil || stderrDone != nil {
		select {
		case callbackErr = <-callbackDone:
			callbackDone = nil
			callbackErr = errors.Join(callbackErr, pipes.inputWrite.Close())
			if callbackErr != nil {
				stopOwned()
			} else if processDone != nil {
				startGrace()
			}
		case processErr = <-processDone:
			processDone = nil
			expired = nil
			if grace != nil {
				grace.Stop()
			}
			cancelCallback()
		case stderrErr = <-stderrDone:
			stderrDone = nil
			if stderrErr != nil {
				cancelCallback()
				stopOwned()
			}
		case <-stopRequested:
			stopRequested = nil
			startGrace()
		case <-expired:
			expired = nil
			stopOwned()
			if stopContext.Err() == nil {
				protocolErr = errors.Join(errors.New("native exchange did not finish after protocol closure"), context.DeadlineExceeded)
			}
		}
	}
	result.Stderr, result.StderrTruncated = append([]byte(nil), stderr.data...), stderr.truncated
	runErr = errors.Join(callbackErr, processErr, stderrErr, stopErr, protocolErr, stopContext.Err())
	if runErr == nil && cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, runErr
}

func (m *Manager) waitNativeFinalization(control context.Context, cancelCleanup context.CancelFunc, closed <-chan time.Time, pipes nativeExchangePipes, owned *ownedProcess, callbackDone, processDone, stderrDone <-chan error) error {
	return m.waitNativeFinalizationOperations(control, cancelCleanup, closed, nativeFinalizationOperations{
		stop: owned.stop, closeInput: pipes.inputWrite.Close,
		closeOutput: pipes.outputRead.Close, closeStderr: pipes.stderrRead.Close,
	}, callbackDone, processDone, stderrDone)
}

type nativeFinalizationOperations struct {
	stop, closeInput, closeOutput, closeStderr func() error
}

func (m *Manager) waitNativeFinalizationOperations(control context.Context, cancelCleanup context.CancelFunc, closed <-chan time.Time, ops nativeFinalizationOperations, callbackDone, processDone, stderrDone <-chan error) error {
	var deadline time.Time
	var timer *time.Timer
	var expired <-chan time.Time
	var runErr error
	cutoffObserved, timedOut := false, false
	setDeadline := func(at time.Time) {
		if timedOut || (!deadline.IsZero() && !at.Before(deadline)) {
			return
		}
		deadline = at
		if timer == nil {
			timer = time.NewTimer(time.Until(at))
		} else {
			timer.Reset(time.Until(at))
		}
		expired = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	// Each endpoint and stop operation has one owner and a required join. A
	// blocked Close or retirement mutex must not block deadline accounting.
	operations := make(chan error, 4)
	pending := 0
	launch := func(operation func() error) {
		pending++
		go func() { operations <- operation() }()
	}
	inputClosing, outputClosing, terminating := false, false, false
	closeInput := func() {
		if !inputClosing {
			inputClosing = true
			launch(ops.closeInput)
		}
	}
	closeOutput := func() {
		if !outputClosing {
			outputClosing = true
			launch(ops.closeOutput)
		}
	}
	terminate := func() {
		if terminating {
			return
		}
		terminating = true
		cancelCleanup()
		launch(ops.stop)
		closeInput()
		closeOutput()
		launch(ops.closeStderr)
	}
	recordCleanup := func(err error) {
		if err != nil {
			runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, err)
			m.CloseAdmission()
			m.recordCleanup(err, err)
		}
	}
	stopRequested := control.Done()
	for {
		if !cutoffObserved && control.Err() != nil {
			cutoffObserved = true
			stopRequested = nil
			setDeadline(time.Now().Add(5 * time.Second))
		}
		// The timestamp belongs to validation, not notification consumption.
		select {
		case at := <-closed:
			closed = nil
			setDeadline(at)
		default:
		}
		if !timedOut && !deadline.IsZero() && !time.Now().Before(deadline) {
			timedOut = true
			expired = nil
			runErr = errors.Join(runErr, ErrNativeExchangeExpired, context.DeadlineExceeded)
			terminate()
		}
		if callbackDone == nil && processDone == nil && stderrDone == nil && pending == 0 {
			return errors.Join(runErr, control.Err())
		}
		select {
		case err := <-callbackDone:
			callbackDone = nil
			runErr = errors.Join(runErr, err)
			closeInput()
			closeOutput()
			if err != nil {
				terminate()
			}
		case err := <-processDone:
			processDone = nil
			runErr = errors.Join(runErr, err)
		case err := <-stderrDone:
			stderrDone = nil
			runErr = errors.Join(runErr, err)
			if err != nil {
				terminate()
			}
		case err := <-operations:
			pending--
			recordCleanup(err)
		case at := <-closed:
			closed = nil
			setDeadline(at)
		case <-stopRequested:
			// Observe and fix the cutoff at the top of the loop.
		case <-expired:
			// Expiry is accounted before any potentially blocking operation.
		}
	}
}
