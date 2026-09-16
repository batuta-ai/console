//go:build unix

package session

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	return f, err
}

func resizePTY(f *os.File, cols, rows int) error {
	return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
