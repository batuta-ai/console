//go:build windows

package api

import (
	"os"
	"syscall"
	"unsafe"
)

func holdObservationJournal(file *os.File) (func() error, error) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	lock := kernel.NewProc("LockFileEx")
	unlock := kernel.NewProc("UnlockFileEx")
	overlapped := syscall.Overlapped{Offset: (256 << 20) + 1}
	result, _, err := lock.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return nil, err
	}
	return func() error {
		result, _, err := unlock.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		if result == 0 {
			return err
		}
		return nil
	}, nil
}
