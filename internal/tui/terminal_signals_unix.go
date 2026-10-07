//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package tui

import (
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

func terminalSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGTSTP}
}
func terminalIsSuspend(s os.Signal) bool { return s == syscall.SIGTSTP }

type terminalSuspendCommand struct{}

func (terminalSuspendCommand) SetStdin(io.Reader)  {}
func (terminalSuspendCommand) SetStdout(io.Writer) {}
func (terminalSuspendCommand) SetStderr(io.Writer) {}
func (terminalSuspendCommand) Run() error {
	// Exec has restored the host. Stop only this UI, never its daemon or the
	// caller's process group; CONT resumes Exec's normal terminal restoration.
	// On Linux, Kill can return before SIGSTOP stops the process. Keep Exec
	// blocked until CONT so it cannot put the host back into raw mode early.
	continued := make(chan os.Signal, 1)
	signal.Notify(continued, syscall.SIGCONT)
	defer signal.Stop(continued)
	if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
		return err
	}
	<-continued
	return nil
}
func terminalSuspend() tea.Cmd {
	return tea.Exec(terminalSuspendCommand{}, func(err error) tea.Msg { return terminalResumed{err: err} })
}
