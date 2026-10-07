//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !aix

package tui

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"os"
)

func terminalSignals() []os.Signal     { return []os.Signal{os.Interrupt} }
func terminalIsSuspend(os.Signal) bool { return false }
func terminalSuspend() tea.Cmd {
	return func() tea.Msg {
		return terminalResumed{err: fmt.Errorf("terminal suspend unsupported on this platform")}
	}
}
