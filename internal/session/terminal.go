package session

import (
	"os"
	"syscall"
)

// A terminal has a readable output stream and a separately controlled input
// endpoint on Windows. Unix continues to use its original bidirectional file.
type terminalPTY interface {
	Read([]byte) (int, error)
	Close() error
	Name() string
	Stat() (os.FileInfo, error)
	SyscallConn() (syscall.RawConn, error)
}

func (l *live) terminalPTY() terminalPTY {
	if l.terminal != nil {
		return l.terminal
	}
	return l.pty
}
