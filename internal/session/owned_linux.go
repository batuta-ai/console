package session

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func signalOwnedGroup(pgid int) error { return unix.Kill(-pgid, unix.SIGKILL) }

func observeOwnedExit(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			return fmt.Errorf("PID %d: %w: %w", pid, errAnchorLost, err)
		}
		if err != nil {
			return fmt.Errorf("observe owned PID %d: %w", pid, err)
		}
		return nil
	}
}
