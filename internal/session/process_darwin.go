package session

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func InspectProcess(pid int) (ProcessObservation, error) {
	if pid <= 0 {
		return ProcessObservation{}, fmt.Errorf("invalid process PID %d", pid)
	}
	// The singular API reports EIO for a zero-length (absent process) result.
	records, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return ProcessObservation{}, fmt.Errorf("inspect PID %d: %w", pid, err)
	}
	if len(records) == 0 {
		return ProcessObservation{Missing: true}, nil
	}
	if len(records) != 1 || int(records[0].Proc.P_pid) != pid {
		return ProcessObservation{}, fmt.Errorf("unexpected process record for PID %d", pid)
	}
	p := records[0].Proc
	// Darwin sys/proc.h defines SZOMB as 5.
	if p.P_stat == 5 {
		return ProcessObservation{Missing: true}, nil
	}
	birth := p.P_starttime
	if birth.Sec <= 0 || birth.Usec < 0 || birth.Usec >= 1000000 {
		return ProcessObservation{}, fmt.Errorf("unavailable birth identity for PID %d", pid)
	}
	return ProcessObservation{Identity: fmt.Sprintf("darwin:%d:%d:%d", pid, birth.Sec, birth.Usec)}, nil
}
