//go:build !darwin && !linux && !windows

package session

import (
	"fmt"
	"os/exec"
)

type ownedProcess struct{}

func newOwnedProcess(cmd *exec.Cmd) *ownedProcess    { return &ownedProcess{} }
func (p *ownedProcess) stop() error                  { return fmt.Errorf("owned process group termination unsupported") }
func waitOwned(cmd *exec.Cmd, p *ownedProcess) error { return cmd.Wait() }
