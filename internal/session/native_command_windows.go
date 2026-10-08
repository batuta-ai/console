package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
)

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
			if err := os.RemoveAll(privateHome); err != nil {
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
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if command.Observer != nil {
		cmd.Stdout = io.MultiWriter(stdout, command.Observer)
	}
	if command.StderrObserver != nil {
		cmd.Stderr = io.MultiWriter(stderr, command.StderrObserver)
	}
	if graceful {
		code, err := m.runWindowsConsoleCommand(ctx, cmd)
		result = publication.CommandResult{Stdout: append([]byte(nil), stdout.data...), Stderr: append([]byte(nil), stderr.data...), ExitCode: -1, StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated}
		if err == nil && ctx.Err() == nil {
			result.ExitCode = code
		}
		return result, errors.Join(err, ctx.Err())
	}
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		return result, err
	}
	m.mu.Lock()
	m.probes[owned] = struct{}{}
	closing := m.closing
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		ownership, wait := waitOwnedOutcome(cmd, owned)
		cleanup := errors.Join(ownership, wait)
		if cleanup != nil {
			cleanup = errors.Join(ErrNativeCleanupUnresolved, cleanup)
			m.CloseAdmission()
			m.recordCleanup(cleanup, cleanup)
		}
		done <- cleanup
	}()
	stop := func() error {
		err := owned.stop()
		m.recordCleanup(err, err)
		return err
	}
	if closing {
		_ = stop()
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.Join(stop(), <-done)
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
