//go:build unix

package session

import (
	"context"
	"os"
	"os/exec"
	"runtime"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/creack/pty"
)

func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	return f, err
}

func resizePTY(f *os.File, cols, rows int) error {
	return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// ObservePTY allocates and resizes a native PTY without starting a process.
// Availability is an observation at request time, not a future launch guarantee.
func ObservePTY(ctx context.Context) domain.PTYCapability {
	r := domain.PTYCapability{Supported: runtime.GOOS == "darwin" || runtime.GOOS == "linux", Code: "platform_unsupported", Qualification: domain.CapabilityFact{Status: "unsupported", Code: "platform_unsupported"}}
	if !r.Supported {
		return r
	}
	r.Qualification = domain.CapabilityFact{Status: "pending", Code: "native_operation_qualification_pending"}
	if ctx.Err() != nil {
		r.Code = "check_cancelled"
		return r
	}
	master, slave, err := pty.Open()
	if err != nil {
		r.Code = "pty_unavailable"
		return r
	}
	resizeErr := resizePTY(master, 80, 24)
	slaveErr := slave.Close()
	masterErr := master.Close()
	if ctx.Err() != nil {
		r.Code = "check_cancelled"
		return r
	}
	if resizeErr != nil || slaveErr != nil || masterErr != nil {
		r.Code = "pty_unavailable"
		return r
	}
	r.Available = true
	r.Code = "pty_available"
	return r
}
