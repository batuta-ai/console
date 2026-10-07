//go:build !darwin && !linux

package session

import (
	"context"
	"os"
)

func prepareTerminalPTY(f *os.File) (*os.File, error)         { return f, ErrTerminalInputUnsupported }
func writeTerminalChunk(f *os.File, data []byte) (int, error) { return 0, ErrTerminalInputUnsupported }
func terminalWouldBlock(err error) bool                       { return false }
func waitTerminalWritable(ctx context.Context, revoked <-chan struct{}, f *os.File) error {
	return ErrTerminalInputUnsupported
}
