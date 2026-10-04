//go:build darwin || linux

package batuta

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openSetupFile(root *os.Root, path string) (*os.File, error) {
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, &os.PathError{Op: "openat", Path: path, Err: os.ErrInvalid}
		}
	}
	base, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	parent := int(base.Fd())
	// Root.OpenFile resolves in-root symlinks even with O_NOFOLLOW. Walk from
	// its anchored directory descriptor instead, rejecting each symlink at open.
	for _, part := range parts[:len(parts)-1] {
		fd, err := unix.Openat(parent, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, &os.PathError{Op: "openat", Path: path, Err: err}
		}
		defer unix.Close(fd)
		parent = fd
	}
	// Nonblocking open prevents a replacement FIFO from hanging the reader.
	fd, err := unix.Openat(parent, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
