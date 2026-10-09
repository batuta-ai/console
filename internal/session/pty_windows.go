//go:build windows

package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/batuta-ai/console/internal/domain"
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

func ObservePTY(ctx context.Context) domain.PTYCapability {
	return domain.PTYCapability{Code: "platform_unsupported", Qualification: domain.CapabilityFact{Status: "unsupported", Code: "platform_unsupported"}}
}
