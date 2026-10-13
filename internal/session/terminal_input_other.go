//go:build !darwin && !linux && !windows

package session

import (
	"context"
)

func prepareTerminalPTY(f terminalPTY) (terminalPTY, error) { return f, ErrTerminalInputUnsupported }
func writeTerminalChunk(f terminalPTY, data []byte) (int, error) {
	return 0, ErrTerminalInputUnsupported
}
func terminalWouldBlock(err error) bool { return false }
func waitTerminalWritable(ctx context.Context, revoked <-chan struct{}, f terminalPTY) error {
	return ErrTerminalInputUnsupported
}
