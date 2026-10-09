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
}

func newNativeExchangePipes() (nativeExchangePipes, error) {
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		return nativeExchangePipes{}, err
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		return nativeExchangePipes{}, errors.Join(err, inputRead.Close(), inputWrite.Close())
	}
	return nativeExchangePipes{
		inputRead: &nativeExchangeFile{File: inputRead}, inputWrite: &nativeExchangeFile{File: inputWrite},
		outputRead: &nativeExchangeFile{File: outputRead}, outputWrite: &nativeExchangeFile{File: outputWrite},
	}, nil
}

func (p nativeExchangePipes) Close() error {
	return errors.Join(p.inputRead.Close(), p.inputWrite.Close(), p.outputRead.Close(), p.outputWrite.Close())
}

func (r NativeRunner) runNativeExchange(ctx context.Context, command publication.Command, handler func(context.Context, io.Reader, io.WriteCloser) error) (result publication.CommandResult, runErr error) {
	result.ExitCode = -1
	if r.Manager == nil || handler == nil || !filepath.IsAbs(command.Executable) || r.Graceful || len(command.Stdin) != 0 || command.Observer != nil || command.StdoutLimit != 0 || command.StderrLimit < 0 || command.StderrLimit > 16<<20 {
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
	// Keeping the parent endpoints out of Cmd's I/O list permits protocol drain
	// to finish independently of Cmd.Wait closing its own copy goroutines.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pipes.inputRead.File, pipes.outputWrite.File, stderr
	cmd.WaitDelay = 250 * time.Millisecond
	if command.StderrObserver != nil {
		cmd.Stderr = io.MultiWriter(stderr, command.StderrObserver)
	}
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		return result, err
	}
	if err := errors.Join(pipes.inputRead.Close(), pipes.outputWrite.Close()); err != nil {
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
	go func() { callbackDone <- handler(callbackContext, pipes.outputRead, pipes.inputWrite) }()
	processDone := make(chan error, 1)
	go func() {
		ownership, wait := waitOwnedOutcome(cmd, owned)
		cleanup := errors.Join(ownership, wait)
		if cleanup != nil {
			m.CloseAdmission()
			m.recordCleanup(cleanup, ownership)
		}
		processDone <- cleanup
	}()
	var callbackErr, processErr, stopErr error
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
	for callbackDone != nil || processDone != nil {
		select {
		case callbackErr = <-callbackDone:
			callbackDone = nil
			callbackErr = errors.Join(callbackErr, pipes.inputWrite.Close())
			if callbackErr != nil {
				stopErr = errors.Join(stopErr, owned.stop())
			} else if processDone != nil {
				startGrace()
			}
		case processErr = <-processDone:
			processDone = nil
			cancelCallback()
		case <-stopRequested:
			stopRequested = nil
			startGrace()
		case <-expired:
			expired = nil
			stopErr = errors.Join(stopErr, owned.stop())
			if stopContext.Err() == nil {
				stopErr = errors.Join(stopErr, errors.New("native exchange did not finish after protocol closure"), context.DeadlineExceeded)
			}
		}
	}
	m.recordCleanup(stopErr, stopErr)
	result.Stderr, result.StderrTruncated = append([]byte(nil), stderr.data...), stderr.truncated
	runErr = errors.Join(callbackErr, processErr, stopErr, stopContext.Err())
	if runErr == nil && cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, runErr
}
