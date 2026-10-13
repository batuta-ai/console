package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func ownedHostError() error {
	if err := procAbsenceObservable("/proc"); err != nil {
		return errors.Join(ErrOwnedHostUnsupported, err)
	}
	return nil
}

func signalOwnedGroup(pgid int) error { return unix.Kill(-pgid, unix.SIGKILL) }

// The final signal has been sent, but Cmd.Wait has not reaped the leader yet.
// Its retained PID still anchors this group; observations grant no authority.
func waitOwnedGroupExit(pgid int) error {
	return waitProcGroupExit("/proc", pgid)
}

func waitProcGroupExit(root string, pgid int) error {
	if pgid <= 1 {
		return fmt.Errorf("invalid owned process group %d", pgid)
	}
	if err := procAbsenceObservable(root); err != nil {
		return err
	}
	deadline := time.NewTimer(250 * time.Millisecond)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		entries, err := os.ReadDir(root)
		if err != nil {
			return fmt.Errorf("inspect owned group %d: %w", pgid, err)
		}
		live := false
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 {
				continue
			}
			stat, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
			if os.IsNotExist(err) || errors.Is(err, unix.ESRCH) {
				continue
			}
			if err != nil {
				return fmt.Errorf("inspect owned group %d PID %d: %w", pgid, pid, err)
			}
			fields, err := procStatFields(pid, stat)
			if err != nil {
				return fmt.Errorf("inspect owned group %d: %w", pgid, err)
			}
			group, err := strconv.Atoi(fields[2])
			if err != nil || group < -1 {
				return fmt.Errorf("invalid proc group record for PID %d", pid)
			}
			// procfs leaves pgrp=-1 when exit races with sighand observation.
			// Membership is unresolved until a subsequent scan, not absent.
			if group == -1 {
				live = true
				continue
			}
			if group == pgid {
				threads, err := strconv.Atoi(fields[17])
				if err != nil || threads < 0 {
					return fmt.Errorf("invalid proc thread count for PID %d", pid)
				}
				// A zombie group leader can retain other threads that are exiting.
				if !strings.Contains("ZXx", fields[0]) || threads > 1 {
					live = true
				}
			}
		}
		if !live {
			return nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return fmt.Errorf("owned process group %d: %w", pgid, errGroupExiting)
		}
	}
}

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
