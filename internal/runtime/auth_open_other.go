//go:build !darwin && !windows

package runtime

import "golang.org/x/sys/unix"

func openRuntimeFileAt(parent int, name string, flags int) (int, error) {
	return unix.Openat(parent, name, flags, 0600)
}
