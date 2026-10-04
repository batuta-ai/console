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

func resizePTY(f *os.File, cols, rows int) error {
	return fmt.Errorf("pty: not qualified on windows yet")
}

func ObservePTY(ctx context.Context) domain.PTYCapability {
	return domain.PTYCapability{Code: "platform_unsupported", Qualification: domain.CapabilityFact{Status: "unsupported", Code: "platform_unsupported"}}
}
