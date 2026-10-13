//go:build darwin || linux

package session

import (
	"context"
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Rewrap the existing master, not a new PTY. O_NONBLOCK must be set before
// os.NewFile registers it with the runtime poller, including on Darwin.
func prepareTerminalPTY(f terminalPTY) (terminalPTY, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return f, errors.Join(ErrTerminalInputUnsupported, err)
	}
	fd := -1
	var setupErr error
	err = raw.Control(func(original uintptr) {
		fd, setupErr = unix.Dup(int(original))
		if setupErr == nil {
			unix.CloseOnExec(fd)
			setupErr = unix.SetNonblock(fd, true)
		}
	})
	if err != nil || setupErr != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return f, errors.Join(ErrTerminalInputUnsupported, err, setupErr)
	}
	wrapped := os.NewFile(uintptr(fd), f.Name())
	if err = f.Close(); err != nil {
		return wrapped, errors.Join(ErrTerminalInputUnsupported, err)
	}
	return wrapped, nil
}

func writeTerminalChunk(f terminalPTY, data []byte) (int, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	n := 0
	var writeErr error
	err = raw.Control(func(fd uintptr) { n, writeErr = unix.Write(int(fd), data) })
	if err != nil {
		return n, err
	}
	return n, writeErr
}

func terminalWouldBlock(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR)
}

func waitTerminalWritable(ctx context.Context, revoked <-chan struct{}, f terminalPTY) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-revoked:
			return os.ErrPermission
		default:
		}
		var pollErr error
		var ready int
		// A bounded poll keeps cancellation independent of the session mutex and
		// prevents closing/reusing the descriptor while the kernel inspects it.
		err = raw.Control(func(fd uintptr) { ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 20) })
		if err != nil {
			return err
		}
		if pollErr != nil && !errors.Is(pollErr, syscall.EINTR) {
			return pollErr
		}
		if ready > 0 {
			return nil
		}
	}
}
