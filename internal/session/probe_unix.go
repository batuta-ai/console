//go:build darwin || linux || windows

package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
)

const probeOutputLimit = 32 << 10

type probeOutput struct {
	mu       sync.Mutex
	used     int
	stdout   []byte
	exceeded bool
	overflow chan struct{}
}

type probeWriter struct {
	output *probeOutput
	stdout bool
}

func (w probeWriter) Write(data []byte) (int, error) {
	p := w.output
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exceeded || len(data) > probeOutputLimit-p.used {
		if !p.exceeded {
			p.exceeded = true
			close(p.overflow)
		}
		return 0, ErrProbeOutputLimit
	}
	p.used += len(data)
	if w.stdout {
		p.stdout = append(p.stdout, data...)
	}
	return len(data), nil
}

// Probe runs an internal diagnostic without a PTY, transcript or session row.
// Cancellation bounds the caller; Manager retains cleanup until Shutdown drains.
func (m *Manager) Probe(ctx context.Context, argv, env []string, dir string) (result ProbeResult, probeErr error) {
	result.ExitCode = -1
	if len(argv) == 0 || argv[0] == "" {
		return result, ErrInvalidProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return result, ErrProbeClosing
	}
	m.active.Add(1)
	m.mu.Unlock()
	spawned := false
	privateHome := ""
	defer func() {
		if !spawned {
			if privateHome != "" {
				homeErr := os.RemoveAll(privateHome)
				probeErr = errors.Join(probeErr, homeErr)
				m.recordCleanup(homeErr, homeErr)
			}
			m.active.Done()
		}
	}()
	if accounts.ExplicitHome(env) == "" {
		privateEnv, home, err := accounts.SessionEnvironment()
		if err != nil {
			return result, fmt.Errorf("prepare diagnostic environment: %w", err)
		}
		privateHome = home
		env = append(append([]string{}, env...), privateEnv...)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	output := &probeOutput{overflow: make(chan struct{})}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = accounts.RuntimeEnvironment(os.Environ(), env)
	cmd.Dir = dir
	cmd.Stdout = probeWriter{output: output, stdout: true}
	cmd.Stderr = probeWriter{output: output}
	cmd.WaitDelay = 250 * time.Millisecond
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		return result, err
	}
	m.mu.Lock()
	m.probes[owned] = struct{}{}
	closing := m.closing
	m.mu.Unlock()
	spawned = true
	type completion struct {
		result ProbeResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		ownershipErr, waitErr := waitOwnedOutcome(cmd, owned)
		if privateHome != "" {
			ownershipErr = errors.Join(ownershipErr, os.RemoveAll(privateHome))
		}
		cleanupErr := errors.Join(ownershipErr, waitErr)
		// Output-limit failures are diagnostic outcomes, not unresolved cleanup.
		if errors.Is(waitErr, ErrProbeOutputLimit) {
			cleanupErr = ownershipErr
		}
		m.recordCleanup(cleanupErr, ownershipErr)
		completed := ProbeResult{ExitCode: -1}
		if cmd.ProcessState != nil {
			completed.ExitCode = cmd.ProcessState.ExitCode()
		}
		outcomeErr := errors.Join(ownershipErr, waitErr)
		output.mu.Lock()
		if output.exceeded {
			outcomeErr = errors.Join(outcomeErr, ErrProbeOutputLimit)
		}
		if completed.ExitCode != 0 {
			outcomeErr = errors.Join(outcomeErr, ErrProbeExit)
		}
		if outcomeErr == nil {
			completed.Output = append([]byte(nil), output.stdout...)
		}
		output.mu.Unlock()
		m.mu.Lock()
		delete(m.probes, owned)
		m.mu.Unlock()
		done <- completion{completed, outcomeErr}
		m.active.Done()
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
	case completed := <-done:
		if err := ctx.Err(); err != nil {
			return result, errors.Join(err, completed.err)
		}
		return completed.result, completed.err
	case <-output.overflow:
		return result, errors.Join(ErrProbeOutputLimit, stop())
	case <-ctx.Done():
		return result, errors.Join(ctx.Err(), stop())
	}
}
