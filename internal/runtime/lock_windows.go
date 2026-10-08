package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const (
	windowsOwnershipOffset = 0x7fffffff
	windowsMetadataOffset  = windowsOwnershipOffset - 1
	windowsLockWaitMillis  = 2000
	windowsMaxLockBytes    = 256 << 10
)

// Waiting callers pass an asynchronous descriptor. Cancellation is drained
// before the OVERLAPPED structure or its event can be released.
func windowsLockRange(f *os.File, offset uint32, exclusive, nonblocking bool) error {
	var flags uint32
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	overlap := windows.Overlapped{Offset: offset}
	handle := windows.Handle(f.Fd())
	if nonblocking {
		return windows.LockFileEx(handle, flags|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	// Suppress completion-port notification for this explicitly waited request.
	overlap.HEvent = event | 1
	err = windows.LockFileEx(handle, flags, 0, 1, 0, &overlap)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return err
	}
	status, waitErr := windows.WaitForSingleObject(event, windowsLockWaitMillis)
	var transferred uint32
	if waitErr == nil && status == windows.WAIT_OBJECT_0 {
		return windows.GetOverlappedResult(handle, &overlap, &transferred, true)
	}
	if waitErr == nil {
		if status == uint32(windows.WAIT_TIMEOUT) {
			waitErr = os.ErrDeadlineExceeded
		} else {
			waitErr = fmt.Errorf("unexpected Windows lock wait status: %d", status)
		}
	}
	cancelErr := windows.CancelIoEx(handle, &overlap)
	if errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
		cancelErr = nil
	}
	drainErr := windows.GetOverlappedResult(handle, &overlap, &transferred, true)
	if drainErr == nil {
		// A grant racing the deadline must not leave an abandoned held lock.
		drainErr = windowsUnlockRange(f, offset)
	} else if errors.Is(drainErr, windows.ERROR_OPERATION_ABORTED) {
		drainErr = nil
	}
	return errors.Join(waitErr, cancelErr, drainErr)
}

func windowsUnlockRange(f *os.File, offset uint32) error {
	overlap := windows.Overlapped{Offset: offset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlap)
}

func readLockBytes(path string) (data []byte, err error) {
	dir, err := OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, dir.Close())
		if err != nil {
			data = nil
		}
	}()
	lease, err := windowsPrivateFileAtMode(dir, filepath.Base(path), windows.FILE_GENERIC_READ, windows.FILE_OPEN, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, lease.Close())
		if err != nil {
			data = nil
		}
	}()
	if err := windowsLockRange(lease, windowsMetadataOffset, false, false); err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, windowsUnlockRange(lease, windowsMetadataOffset))
		if err != nil {
			data = nil
		}
	}()
	f, err := windowsPrivateFileAt(dir, filepath.Base(path), windows.FILE_GENERIC_READ, windows.FILE_OPEN)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			data = nil
		}
	}()
	data, err = io.ReadAll(io.LimitReader(f, windowsMaxLockBytes+1))
	if err == nil && len(data) > windowsMaxLockBytes {
		return nil, fmt.Errorf("daemon lock metadata exceeds the bounded size")
	}
	return data, err
}

func writeWindowsLockMetadata(dir, f *os.File, name string, data []byte) (err error) {
	lease, err := windowsPrivateFileAtMode(dir, name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN, false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	if err := windowsLockRange(lease, windowsMetadataOffset, true, false); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windowsUnlockRange(lease, windowsMetadataOffset)) }()
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
