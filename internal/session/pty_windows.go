//go:build windows

package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/windows"
)

func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	return nil, fmt.Errorf("pty: not qualified on windows yet")
}

func resizePTY(f terminalPTY, cols, rows int) error {
	console, ok := f.(*windowsTerminal)
	if !ok || console == nil {
		return os.ErrInvalid
	}
	return console.resize(cols, rows)
}

var windowsPTYObservationLibrary = windows.NewLazySystemDLL("kernel32.dll")

func ObservePTY(ctx context.Context) domain.PTYCapability {
	r := domain.PTYCapability{Supported: true, Code: "pty_unavailable", Qualification: domain.CapabilityFact{Status: "pending", Code: "native_operation_qualification_pending"}}
	if ctx.Err() != nil {
		r.Code = "check_cancelled"
		return r
	}
	for _, name := range []string{"CreatePseudoConsole", "ResizePseudoConsole", "ClosePseudoConsole"} {
		if err := windowsPTYObservationLibrary.NewProc(name).Find(); err != nil {
			return r
		}
	}
	terminal, err := newWindowsTerminal(80, 24, func() error { return nil })
	if err != nil {
		return r
	}
	resizeErr := terminal.resize(80, 24)
	closeErr := terminal.Close()
	if ctx.Err() != nil {
		r.Code = "check_cancelled"
		return r
	}
	if resizeErr != nil || closeErr != nil {
		return r
	}
	r.Available = true
	r.Code = "pty_available"
	return r
}
