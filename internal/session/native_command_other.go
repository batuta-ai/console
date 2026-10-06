//go:build !darwin && !linux

package session

import (
	"context"
	"github.com/batuta-ai/core/publication"
)

func (m *Manager) runNativeCommand(context.Context, publication.Command, []string, bool) (publication.CommandResult, error) {
	return publication.CommandResult{ExitCode: -1}, ErrProbeUnsupported
}
