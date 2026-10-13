package session

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The x/sys Job information wrappers take uintptr, so the buffer must remain
// pinned across the intermediate Go call as well as the synchronous syscall.
func queryWindowsJob(job windows.Handle, class int32, data unsafe.Pointer, size uint32) error {
	var pin runtime.Pinner
	pin.Pin(data)
	defer pin.Unpin()
	return windows.QueryInformationJobObject(job, class, uintptr(data), size, nil)
}

func setWindowsJob(job windows.Handle, class uint32, data unsafe.Pointer, size uint32) error {
	var pin runtime.Pinner
	pin.Pin(data)
	defer pin.Unpin()
	_, err := windows.SetInformationJobObject(job, class, uintptr(data), size)
	return err
}
