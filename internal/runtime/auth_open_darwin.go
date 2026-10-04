package runtime

import "golang.org/x/sys/unix"

func openRuntimeFileAt(parent int, name string, flags int) (int, error) {
	fd, err := unix.Openat(parent, name, flags, 0600)
	if flags&unix.O_CREAT != 0 && flags&unix.O_EXCL == 0 {
		// WORKAROUND: concurrent Darwin first-create returned ENOENT against a
		// retained parent. Preserve flags/FD and report persistent errors.
		for attempt := 1; attempt < 3 && err == unix.ENOENT; attempt++ {
			fd, err = unix.Openat(parent, name, flags, 0600)
		}
	}
	return fd, err
}
