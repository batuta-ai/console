package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/core/publication"
)

// Core's Git clients expect ExecRunner semantics. NativeRunner keeps completed
// exits as data for providers and shells, so adapt only direct Git commands.
// Authorization, process ownership and stream joins still belong to commands.
type nativeCoreGitRunner struct{ commands nativeCoreCommands }

func (r nativeCoreGitRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	result, err := r.commands.Run(ctx, command)
	base := filepath.Base(command.Executable)
	if err == nil && result.ExitCode != 0 && (strings.EqualFold(base, "git") || strings.EqualFold(base, "git.exe")) {
		return result, fmt.Errorf("native Core Git command exited with code %d", result.ExitCode)
	}
	return result, err
}
