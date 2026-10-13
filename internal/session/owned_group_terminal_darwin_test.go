package session

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinGroupExitWaitsForRealTerminalState(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if group, err := unix.Getpgid(cmd.Process.Pid); err != nil || group != cmd.Process.Pid {
		t.Fatalf("fixture ownership: %d %v", group, err)
	}
	if err := waitOwnedGroupExit(cmd.Process.Pid); !errors.Is(err, errGroupExiting) {
		t.Fatalf("live group reported terminal: %v", err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("observation signaled the fixture: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitForUnreapedExit(t, cmd.Process.Pid)
	if err := waitOwnedGroupExit(cmd.Process.Pid); err != nil {
		t.Fatalf("confirmed terminal group: %v", err)
	}
}
