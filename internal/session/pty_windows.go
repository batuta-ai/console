//go:build windows

package session

import (
	"fmt"
	"os"
	"os/exec"
)

func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	return nil, fmt.Errorf("pty: not qualified on windows yet")
}

func resizePTY(f *os.File, cols, rows int) error {
	return fmt.Errorf("pty: not qualified on windows yet")
}
