package session

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Called only under the active unreaped-child authority's mutex. Observation
// resolves Darwin's zombie-only EPERM; it never supplies signaling authority.
func signalOwnedGroup(pgid int) error {
	err := unix.Kill(-pgid, unix.SIGKILL)
	if !errors.Is(err, unix.EPERM) {
		return err
	}
	// XNU killpg1 excludes SZOMB and returns EPERM when no eligible member was
	// found. kern.proc.pgrp includes both live and zombie lists, without a UID
	// filter. The x/sys slice API rejects incomplete records and retries ENOMEM.
	records, inspectErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if inspectErr != nil {
		return errors.Join(err, fmt.Errorf("inspect anchored group %d: %w", pgid, inspectErr))
	}
	pending := false
	for _, record := range records {
		if record.Proc.P_pid <= 0 || int(record.Eproc.Pgid) != pgid {
			return fmt.Errorf("anchored group %d has nonterminal or unverifiable record PID %d state %d flags %#x: %w", pgid, record.Proc.P_pid, record.Proc.P_stat, record.Proc.P_flag, err)
		}
		if record.Proc.P_stat == 5 {
			continue
		}
		// Darwin sys/proc.h: P_WEXIT advertises exit in progress, not a
		// waitable zombie. Final group cleanup must still prove termination.
		if record.Proc.P_flag&0x2000 == 0 {
			return fmt.Errorf("anchored group %d has nonterminal or unverifiable record PID %d state %d flags %#x: %w", pgid, record.Proc.P_pid, record.Proc.P_stat, record.Proc.P_flag, err)
		}
		pending = true
	}
	if pending {
		return errors.Join(err, fmt.Errorf("anchored group %d: %w", pgid, errGroupExiting))
	}
	return nil
}

func waitOwnedGroupExit(int) error { return nil }

func observeOwnedExit(pid int) error {
	// Darwin x/sys has no Waitid wrapper. kern.proc.pid exposes SZOMB without
	// consuming wait status (XNU sys/proc.h); Cmd.Wait remains the sole reaper.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
		if err != nil {
			return fmt.Errorf("observe owned PID %d: %w", pid, err)
		}
		if len(records) != 1 || int(records[0].Proc.P_pid) != pid {
			return fmt.Errorf("PID %d: %w", pid, errAnchorLost)
		}
		if records[0].Proc.P_stat == 5 {
			return nil
		}
		<-ticker.C
	}
}
