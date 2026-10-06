//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
)

type nativeOutput struct {
	limit     int64
	data      []byte
	truncated bool
}

func (w *nativeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - int64(len(w.data))
	if int64(n) > remaining {
		w.truncated = true
	}
	if remaining > 0 {
		if int64(len(p)) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

func (m *Manager) runNativeCommand(ctx context.Context, command publication.Command, explicit []string, graceful bool) (result publication.CommandResult, runErr error) {
	result.ExitCode = -1
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return result, ErrProbeClosing
	}
	m.active.Add(1)
	m.mu.Unlock()
	defer m.active.Done()
	privateHome := ""
	if accounts.ExplicitHome(explicit) == "" {
		env, home, err := accounts.SessionEnvironment()
		if err != nil {
			return result, err
		}
		privateHome = home
		explicit = append(append([]string{}, explicit...), env...)
	}
	defer func() {
		if privateHome != "" {
			err := os.RemoveAll(privateHome)
			if err != nil {
				result.ExitCode = -1
				runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, err)
				m.CloseAdmission()
				m.recordCleanup(runErr, runErr)
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
	stdoutLimit, stderrLimit := command.StdoutLimit, command.StderrLimit
	if stdoutLimit == 0 {
		stdoutLimit = 1 << 20
	}
	if stderrLimit == 0 {
		stderrLimit = 64 << 10
	}
	stdout, stderr := &nativeOutput{limit: stdoutLimit}, &nativeOutput{limit: stderrLimit}
	cmd := exec.Command(command.Executable, command.Args...)
	cmd.Env, cmd.Dir = env, command.Directory
	cmd.Stdin = bytes.NewReader(command.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdout = stdout
	if command.Observer != nil {
		cmd.Stdout = io.MultiWriter(stdout, command.Observer)
	}
	cmd.Stderr = stderr
	if command.StderrObserver != nil {
		cmd.Stderr = io.MultiWriter(stderr, command.StderrObserver)
	}
	if err := cmd.Start(); err != nil {
		return result, err
	}
	owned := newOwnedProcess(cmd)
	m.mu.Lock()
	m.probes[owned] = struct{}{}
	closing := m.closing
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		ownershipErr, waitErr := waitOwnedOutcome(cmd, owned)
		cleanupErr := errors.Join(ownershipErr, waitErr)
		if cleanupErr != nil {
			cleanupErr = errors.Join(ErrNativeCleanupUnresolved, cleanupErr)
			m.CloseAdmission()
			m.recordCleanup(cleanupErr, cleanupErr)
		}
		done <- cleanupErr
	}()
	if closing {
		err := owned.stop()
		m.recordCleanup(err, err)
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		if graceful {
			// The unreaped child is the retained authority. Signal the engine
			// first so it can join workers that own independent process groups.
			owned.mu.Lock()
			var signalErr error
			if !owned.retired {
				signalErr = cmd.Process.Signal(syscall.SIGTERM)
			}
			owned.mu.Unlock()
			timer := time.NewTimer(2 * time.Second)
			select {
			case err = <-done:
				err = errors.Join(signalErr, err)
			case <-timer.C:
				err = errors.Join(publication.ErrReviewCleanupUnresolved, ErrNativeCleanupUnresolved, signalErr, owned.stop(), <-done)
			}
			timer.Stop()
			if err != nil {
				m.CloseAdmission()
				m.recordCleanup(err, err)
			}
		} else {
			stopErr := owned.stop()
			m.recordCleanup(stopErr, stopErr)
			err = errors.Join(stopErr, <-done)
		}
	}
	m.mu.Lock()
	delete(m.probes, owned)
	m.mu.Unlock()
	result = publication.CommandResult{Stdout: append([]byte(nil), stdout.data...), Stderr: append([]byte(nil), stderr.data...), ExitCode: -1, StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated}
	if cmd.ProcessState != nil && err == nil && ctx.Err() == nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, errors.Join(err, ctx.Err())
}
