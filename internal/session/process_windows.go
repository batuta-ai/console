package session

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func InspectProcess(pid int) (observation ProcessObservation, inspectErr error) {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return ProcessObservation{}, fmt.Errorf("invalid process PID %d", pid)
	}
	// Opening an observed PID grants query/synchronization rights only.
	// It cannot become the original creation authority used by an owned Job.
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return ProcessObservation{Missing: true}, nil
	}
	if err != nil {
		return ProcessObservation{}, fmt.Errorf("inspect PID %d: %w", pid, err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			observation = ProcessObservation{}
			inspectErr = errors.Join(inspectErr, err)
		}
	}()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return ProcessObservation{}, fmt.Errorf("inspect PID %d birth: %w", pid, err)
	}
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return ProcessObservation{}, fmt.Errorf("inspect PID %d lifetime: %w", pid, err)
	}
	if status == windows.WAIT_OBJECT_0 {
		return ProcessObservation{Missing: true}, nil
	}
	birth := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if status != uint32(windows.WAIT_TIMEOUT) || birth == 0 {
		return ProcessObservation{}, fmt.Errorf("unavailable birth identity for PID %d", pid)
	}
	return ProcessObservation{Identity: fmt.Sprintf("windows:%d:%d", pid, birth)}, nil
}
