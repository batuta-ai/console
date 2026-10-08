package session

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func prepareTerminalPTY(f terminalPTY) (terminalPTY, error) {
	if _, ok := f.(*windowsTerminal); !ok {
		return f, ErrTerminalInputUnsupported
	}
	return f, nil
}

func writeTerminalChunk(f terminalPTY, data []byte) (int, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var written uint32
	var writeErr error
	if err := raw.Control(func(handle uintptr) {
		writeErr = windows.WriteFile(windows.Handle(handle), data, &written, nil)
	}); err != nil {
		// RawConn exposes Go's internal closing error. Only a file whose
		// public descriptor is invalid is classified as a closed terminal.
		if terminal, ok := f.(*windowsTerminal); ok && windows.Handle(terminal.input.Fd()) == windows.InvalidHandle {
			return int(written), errors.Join(os.ErrClosed, err)
		}
		return int(written), err
	}
	// A PIPE_NOWAIT byte write reports a successful zero-byte operation when
	// its buffer is full. No kernel operation or caller buffer remains pending.
	if written == 0 && writeErr == nil && len(data) > 0 {
		writeErr = windows.ERROR_NO_DATA
	}
	return int(written), writeErr
}

func terminalWouldBlock(err error) bool { return errors.Is(err, windows.ERROR_NO_DATA) }

func waitTerminalWritable(ctx context.Context, revoked <-chan struct{}, f terminalPTY) error {
	// Immediate try-writes serialize with ownership changes; the retry wait
	// releases the session mutex and retains no pending native I/O.
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-revoked:
		return os.ErrPermission
	case <-timer.C:
		return nil
	}
}
