//go:build !darwin && !linux

package session

import (
	"fmt"
	"os/exec"
)

func ownedHostError() error { return nil }

type ownedProcess struct{}

func newOwnedProcess(cmd *exec.Cmd) *ownedProcess    { return &ownedProcess{} }
func (p *ownedProcess) stop() error                  { return fmt.Errorf("owned process group termination unsupported") }
func waitOwned(cmd *exec.Cmd, p *ownedProcess) error { return cmd.Wait() }
