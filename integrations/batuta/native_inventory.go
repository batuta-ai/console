package batuta

import (
	"context"
	"fmt"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/inventory"
	coreadapters "github.com/batuta-ai/core/inventory/adapters"
	"github.com/batuta-ai/core/publication"
)

type AccountInventory struct {
	CLI        string
	Executable string
	Runner     publication.CommandRunner
}

// Only public native adapters normalize observed CLI output. Using ProbeRunner
// directly avoids Collector's ambient HOME/config discovery in the daemon.
func CollectAccountInventory(ctx context.Context, workspace string, accounts []AccountInventory) (inventory.InventorySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if len(accounts) == 0 || len(accounts) > 2 {
		return inventory.InventorySnapshot{}, domain.ErrInvalidReference
	}
	seen := map[string]bool{}
	configured := make([]coreadapters.Adapter, 0, len(accounts))
	for _, selected := range accounts {
		if seen[selected.CLI] || selected.Runner == nil {
			return inventory.InventorySnapshot{}, domain.ErrInvalidReference
		}
		seen[selected.CLI] = true
		var adapter coreadapters.Adapter
		var err error
		switch selected.CLI {
		case "codex":
			adapter, err = coreadapters.NewCodex(selected.Executable)
		case "claude":
			adapter, err = coreadapters.NewClaude(selected.Executable)
		default:
			return inventory.InventorySnapshot{}, domain.ErrInvalidReference
		}
		if err != nil {
			return inventory.InventorySnapshot{}, err
		}
		configured = append(configured, adapter)
	}
	executors := make([]inventory.ExecutorSnapshot, 0, len(accounts))
	for i, selected := range accounts {
		adapter := configured[i]
		outputs := map[inventory.ProbeID][]byte{}
		collect := func(specs []inventory.ProbeSpec) error {
			if len(specs) > 64 {
				return fmt.Errorf("native inventory probe budget exceeded")
			}
			runner, err := inventory.NewProbeRunner(selected.Runner, workspace, specs)
			if err != nil {
				return err
			}
			for _, spec := range specs {
				result, err := runner.Run(ctx, spec.ID)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err == nil && result.ExitCode == 0 && !result.Truncated {
					outputs[spec.ID] = result.Output
				}
			}
			return nil
		}
		if err := collect(adapter.StaticSpecs()); err != nil {
			return inventory.InventorySnapshot{}, err
		}
		dynamic, err := adapter.DynamicSpecs(outputs)
		if err != nil {
			return inventory.InventorySnapshot{}, err
		}
		if err := collect(dynamic); err != nil {
			return inventory.InventorySnapshot{}, err
		}
		executors = append(executors, adapter.Normalize(outputs))
	}
	return inventory.NewSnapshot("", executors)
}
