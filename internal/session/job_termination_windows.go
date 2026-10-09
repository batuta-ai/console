package session

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (p *ownedProcess) observeTermination() error {
	var before windowsJobAccounting
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&before)), uint32(unsafe.Sizeof(before)), nil); err != nil {
		return err
	}
	p.terminationTotal = before.TotalProcesses
	var members struct {
		Assigned uint32
		Count    uint32
		IDs      [1024]uintptr
	}
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&members)), uint32(unsafe.Sizeof(members)), nil); err != nil {
		return fmt.Errorf("observe owned Windows job members: %w", err)
	}
	if members.Count != members.Assigned || members.Count > uint32(len(members.IDs)) {
		return errors.New("owned Windows job member observation exceeds its bound")
	}
	for _, id := range members.IDs[:members.Count] {
		// These handles can only observe termination. The original private Job
		// remains the sole authority to terminate its process tree.
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(id))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			return fmt.Errorf("observe owned Windows job process: %w", err)
		}
		var member int32
		ok, _, callErr := windowsProcessInJob.Call(uintptr(handle), uintptr(p.job), uintptr(unsafe.Pointer(&member)))
		if ok == 0 || member != 1 {
			observationErr := errors.New("owned Windows job process observation changed")
			if ok == 0 {
				observationErr = errors.Join(observationErr, callErr)
			}
			return errors.Join(observationErr, windows.CloseHandle(handle))
		}
		p.terminationWaiters = append(p.terminationWaiters, handle)
	}
	var after windowsJobAccounting
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&after)), uint32(unsafe.Sizeof(after)), nil); err != nil {
		return err
	}
	if after.TotalProcesses != p.terminationTotal {
		return errors.New("owned Windows job gained processes during termination observation")
	}
	return nil
}

func (p *ownedProcess) waitTermination(total uint32, deadline time.Time) error {
	var waitErr error
	if total != p.terminationTotal {
		waitErr = errors.New("owned Windows job gained unobserved processes before termination")
	}
	for _, handle := range p.terminationWaiters {
		remaining := time.Until(deadline)
		var milliseconds uint32
		if remaining > 0 {
			milliseconds = uint32((remaining + time.Millisecond - 1) / time.Millisecond)
		}
		status, err := windows.WaitForSingleObject(handle, milliseconds)
		if err == nil && status != windows.WAIT_OBJECT_0 {
			err = fmt.Errorf("owned Windows job process termination wait returned %d: %w", status, context.DeadlineExceeded)
		}
		waitErr = errors.Join(waitErr, err)
	}
	return waitErr
}
