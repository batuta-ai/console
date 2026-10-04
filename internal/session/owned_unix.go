//go:build darwin || linux

package session

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"golang.org/x/sys/unix"
)

var errAnchorLost = errors.New("owned unreaped child is no longer observable")
var errGroupExiting = errors.New("owned group exit is pending verification")

// Only a successful Setsid start may create this authority: it establishes PID =
// PGID. The sole reaper must retire it under mu before calling Cmd.Wait. Neither
// catalog identity nor current membership can establish this authority later.
type ownedProcess struct {
	mu       sync.Mutex
	pgid     int
	retired  bool
	signaled bool
	err      error
}

func newOwnedProcess(cmd *exec.Cmd) *ownedProcess { return &ownedProcess{pgid: cmd.Process.Pid} }

func (p *ownedProcess) stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.signalLocked()
	if !p.retired && errors.Is(err, errGroupExiting) {
		// The caller still must await completed cleanup within its deadline.
		return nil
	}
	return err
}

func (p *ownedProcess) signalLocked() error {
	if p.retired || p.signaled {
		return p.err
	}
	if p.pgid <= 1 {
		p.err = fmt.Errorf("invalid owned process group %d", p.pgid)
		return p.err
	}
	err := signalOwnedGroup(p.pgid)
	// ESRCH says no signalable members remain, not that descendants escaped into
	// other groups have terminated. The unreaped child still anchors this number.
	if err != nil && !errors.Is(err, unix.ESRCH) {
		p.err = fmt.Errorf("kill owned process group %d: %w", p.pgid, err)
		p.signaled = errors.Is(err, errGroupExiting)
		return p.err
	}
	if errors.Is(p.err, errGroupExiting) {
		p.err = nil
	}
	p.signaled = true
	return p.err
}

func (p *ownedProcess) retire(observeErr error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !errors.Is(observeErr, errAnchorLost) {
		// Sweep once more after leader exit, before irrevocably permitting reap.
		p.signaled = false
		_ = p.signalLocked()
	}
	p.retired = true
	return errors.Join(observeErr, p.err)
}

func waitOwned(cmd *exec.Cmd, p *ownedProcess) error {
	cleanupErr, waitErr := waitOwnedOutcome(cmd, p)
	return errors.Join(cleanupErr, waitErr)
}

func waitOwnedOutcome(cmd *exec.Cmd, p *ownedProcess) (error, error) {
	observeErr := observeOwnedExit(cmd.Process.Pid)
	cleanupErr := p.retire(observeErr)
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		waitErr = nil
	}
	return cleanupErr, waitErr
}
