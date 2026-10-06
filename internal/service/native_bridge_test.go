//go:build darwin || linux

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/executor"
)

func nativeHelperFixture(t *testing.T) string {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "consoled")
	cmd := exec.Command("go", "build", "-o", helper, "./cmd/consoled")
	cmd.Dir = filepath.Join("..", "..")
	if bytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatal("native helper build", string(bytes), err)
	}
	return helper
}

func TestNativeReviewBridgeUsesSelectedAccountsAndRejectsForeignModel(t *testing.T) {
	helper := nativeHelperFixture(t)
	selected := nativeAccounts{byCLI: map[string]nativeAccount{}}
	for _, cli := range []string{"codex", "claude"} {
		home := t.TempDir()
		path := filepath.Join(t.TempDir(), cli)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$HOME\"\nprintf '%s' \"$ANTHROPIC_API_KEY\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		selected.byCLI[cli] = nativeAccount{executable: path, env: []string{"HOME=" + home}, adapter: executor.Adapter{Name: cli, Run: cli + ` --model {model} "{brief}"`, Readonly: cli + ` --model {model} "{prompt}"`, BriefLimitLines: 100}, members: []domain.Member{{CLI: cli, Model: "approved"}}}
	}
	private, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	svc := &Service{NativeExecutorExecutable: helper}
	env, err := svc.nativeReviewEnvironment(home, private, selected)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "ambient-must-not-cross")
	for _, cli := range []string{"codex", "claude"} {
		cmd := exec.Command(filepath.Join(home, "bin", cli), "--model", "approved", "review evidence")
		cmd.Env = append(os.Environ(), env...)
		cmd.Dir = t.TempDir()
		output, err := cmd.CombinedOutput()
		if err != nil || string(output) != accounts.ExplicitHome(selected.byCLI[cli].env)+"\n" {
			t.Fatal("selected native review account was not used exclusively", cli, string(output), err)
		}
		cmd = exec.Command(filepath.Join(home, "bin", cli), "--model", "foreign", "review evidence")
		cmd.Env, cmd.Dir = append(os.Environ(), env...), t.TempDir()
		if output, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(output), accounts.ExplicitHome(selected.byCLI[cli].env)) {
			t.Fatal("unbound review model was submitted", cli, string(output), err)
		}
	}
}
