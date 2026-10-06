//go:build darwin || linux

package batuta

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/inventory"
)

func TestNativeInventoryUsesSelectedAccountProcesses(t *testing.T) {
	root, home, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "ambient-account")
	exe := filepath.Join(bin, "codex")
	script := `#!/bin/sh
if [ -n "$OPENAI_API_KEY" ]; then exit 70; fi
printf '%s\n' "$HOME" >> "$HOME/probed-homes"
case "$1" in
--version) printf 'codex-cli 0.160.0\n';;
debug) printf '{"models":[{"slug":"qa-native"}]}\n';;
doctor) printf '{"schema_version":1}\n';;
plugin) printf '{}\n';;
*) exit 71;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	m := session.New(t.TempDir())
	selected := AccountInventory{CLI: "codex", Executable: exe, Runner: session.NativeRunner{Manager: m, Environment: []string{"HOME=" + home}}}
	snapshot, err := CollectAccountInventory(context.Background(), root, []AccountInventory{selected})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Executors) != 1 || snapshot.Executors[0].ID != inventory.ExecutorCodex || snapshot.Executors[0].Availability != inventory.AvailabilityAvailable {
		t.Fatal(snapshot)
	}
	found := false
	for _, capability := range snapshot.Executors[0].Capabilities {
		for _, id := range capability.Identifiers {
			if capability.State == inventory.ResolutionResolved && id == "codex/qa-native" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("actual CLI model evidence missing", snapshot)
	}
	homes, err := os.ReadFile(filepath.Join(home, "probed-homes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(homes) != (home + "\n" + home + "\n" + home + "\n" + home + "\n" + home + "\n" + home + "\n") {
		t.Fatalf("inventory escaped selected HOME: %q", homes)
	}
	if _, err := os.Stat(filepath.Join(root, ".batuta")); !os.IsNotExist(err) {
		t.Fatal("inventory mutated native journal", err)
	}
}
