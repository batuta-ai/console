package adapters_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
)

func TestReadinessRequiresQualifiedCLIAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, adapter, banner, code string
		available, compatible       bool
	}{
		{"codex-current-no-credential", "codex", "codex-cli 0.160.0", "credential_missing", true, true},
		{"claude-current-no-credential", "claude", "2.1.289 (Claude Code)", "credential_missing", true, true},
		{"claude-future-incompatible", "claude", "99.0.0 (Claude Code)", "cli_incompatible", true, false},
		{"codex-missing", "codex", "", "cli_missing", false, false},
		{"outside-matrix", "gemini", "0.160.0", "adapter_unsupported", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			if tc.banner != "" {
				script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + tc.banner + "'; exit 0; fi\nexit 91\n"
				if err := os.WriteFile(filepath.Join(path, tc.adapter), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", path)
			home := filepath.Join(t.TempDir(), "console")
			a := domain.Account{ID: "qa-ready", Adapter: tc.adapter, Connected: true}
			ad, err := adapters.Get(tc.adapter)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ad.IsolateEnv(home, a); err != nil {
				t.Fatal(err)
			}
			logs := t.TempDir()
			m := session.New(logs)
			probe := func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
				r, e := m.Probe(ctx, argv, env, dir)
				return r.Output, r.ExitCode, e
			}
			checker := adapters.NewReadinessChecker(probe, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got := checker.Check(ctx, home, a)
			if got.Ready || got.Authenticated || !got.Connected || got.Available != tc.available || got.CLICompatible != tc.compatible || got.Code != tc.code {
				t.Fatalf("wrong readiness: %+v", got)
			}
			if err := m.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(logs)
			if err != nil || len(entries) != 0 {
				t.Fatalf("readiness probe created transcript: %v %v", entries, err)
			}
		})
	}
}
