//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

type discoveryProcess struct{ process *os.Process }

func bindDiscoveryProcess(pid int, _ string) (*discoveryProcess, error) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		p.Release()
		return nil, err
	}
	return &discoveryProcess{p}, nil
}
func (p *discoveryProcess) release() { p.process.Release() }
func (p *discoveryProcess) wait(deadline time.Time) error {
	// startDaemon owns Wait; this only observes the live handshake's PID.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		err := p.process.Signal(syscall.Signal(0))
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return fmt.Errorf("owned daemon did not exit")
		}
	}
}
func discoverySharingViolation(err error) bool { return false }
