package session

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func waitForUnreapedExit(t *testing.T, pid int) {
	t.Helper()
	// WNOWAIT observes this child's termination without releasing its PID.
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
}
