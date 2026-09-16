//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureDaemon(cmd *exec.Cmd, logFile *os.File) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
}
