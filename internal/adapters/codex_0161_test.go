//go:build darwin || linux

package adapters

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestCodex0161CompatibilityDoesNotAuthenticate(t *testing.T) {
	for _, tc := range []struct {
		banner string
		code   string
	}{
		{"codex-cli 0.161.0", "credential_missing"},
		{"codex-cli 0.161.1", "cli_incompatible"},
	} {
		t.Run(tc.banner, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.banner+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			home := filepath.Join(t.TempDir(), "console")
			account := domain.Account{ID: "selected-local", Adapter: "codex", Connected: true}
			ad, err := Get("codex")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ad.IsolateEnv(home, account); err != nil {
				t.Fatal(err)
			}
			checker := NewReadinessChecker(func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
				cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
				cmd.Env, cmd.Dir = env, dir
				out, err := cmd.Output()
				return out, 0, err
			}, nil)
			got := checker.Check(context.Background(), home, account)
			if got.Code != tc.code || got.Ready || got.Authenticated || got.CredentialConfigured || got.CLICompatible != (tc.code == "credential_missing") {
				t.Fatalf("wrong isolated CLI/authentication distinction: %+v", got)
			}
		})
	}
}
