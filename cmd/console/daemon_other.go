//go:build !unix

package main

import (
	"os"
	"os/exec"
)

func configureDaemon(cmd *exec.Cmd, logFile *os.File) {
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
}
