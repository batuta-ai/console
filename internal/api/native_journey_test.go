//go:build darwin || linux

package api

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
)

func TestNativeApprovedHTTPJourneyUsesOneDeliveryAfterAnswerAndDeletion(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	module := exec.Command(goPath, "list", "-m", "-f", "{{.Dir}}", "github.com/batuta-ai/core")
	directory, err := module.Output()
	if err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(t.TempDir(), "batuta")
	build := exec.Command(goPath, "build", "-ldflags", "-X main.buildVersion=v1.1.0-beta.55", "-o", core, "./cmd/batuta")
	build.Dir = strings.TrimSpace(string(directory))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal("public Core build", string(output), err)
	}
	h, svc := testServer(t)
	svc.CoreExecutable = core
	root, bin, skills := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "codex"), nativeJourneyProviderScript())
	for _, name := range []string{"batuta", "batuta-loop", "batuta-review"} {
		write(filepath.Join(skills, name, "SKILL.md"), "---\nname: "+name+"\ndescription: native HTTP fixture\n---\n")
	}
	skillRoot := filepath.Join(skills, "batuta")
	write(filepath.Join(skillRoot, "adapters/codex.md"), "---\nname: codex\nrun: 'codex --model {model} \"{brief}\"'\nreadonly: 'codex --verify --model {model} \"{prompt}\"'\navailable: 'codex --version'\nmodels: 'codex debug models'\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n")
	write(filepath.Join(skillRoot, "templates/generic.md"), "# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n")
	t.Setenv("BATUTA_SKILLS", skillRoot)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "ambient-must-never-cross")
	write(filepath.Join(root, ".batuta/profile.md"), "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n")
	write(filepath.Join(root, ".batuta/routing.md"), "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | qa-native |\n| medium | * | codex | qa-native |\n| high | * | codex | qa-native |\n| critical | * | self | session |\n")
	planPath := filepath.Join(root, ".batuta/plans/alpha.md")
	write(planPath, "# Plan — Alpha\n**Goal:** Exact HTTP execution\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n")
	write(filepath.Join(root, ".gitignore"), ".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "console-http-qa"}, {"config", "user.email", "qa@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "chore: native HTTP fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
	}
	account := domain.Account{ID: "acct_http", Adapter: "codex", Connected: true, SessionCap: 1}
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	authenticateInitHTTPAccount(t, svc, account, "http-account")
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root}); err != nil {
		t.Fatal(err)
	}
	mission := domain.Mission{ID: "m", WorkspaceID: "w", SquadSnapshot: domain.Squad{ID: "q", Members: []domain.Member{{ID: "maestro", Maestro: true, CLI: "codex", Model: "qa-native", AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}}}}}
	if err := svc.Store.PutMission(mission); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", service.MissionBindingRequest{WorkspaceID: "w", PlanPath: ".batuta/plans/alpha.md"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.ShutdownNative(ctx); err != nil {
			t.Error(err)
		}
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(h)
	defer server.Close()
	c := client.New(server.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	detail, err := c.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := c.ApprovePlan(ctx, detail.Identity)
	if err != nil || approved.Receipt == nil {
		t.Fatal(approved, err)
	}
	request := domain.ExecutionRequest{Target: detail.Identity, ApprovalID: approved.Receipt.ID}
	started, err := c.StartExecution(ctx, request)
	if err != nil || started.Effect != domain.ExecutionStarted {
		t.Fatal("native HTTP start", started, err)
	}
	question := func(after int) domain.PendingQuestion {
		t.Helper()
		for {
			state, err := c.ExecutionState(ctx, "m", "w", request.ApprovalID)
			if err != nil {
				t.Fatal(err)
			}
			if !state.Active && state.Watch != nil && state.Watch.PendingQuestion != nil && state.Watch.PendingQuestion.Target.Execution > after {
				return *state.Watch.PendingQuestion
			}
			select {
			case <-ctx.Done():
				t.Fatal("native HTTP question missing", state)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	first := question(0)
	if err := os.Remove(planPath); err != nil {
		t.Fatal(err)
	}
	c = client.New(server.URL, "test-token")
	recovered, err := c.PlanDetail(ctx, "m", "w")
	if err != nil || recovered.Approval == nil || recovered.Approval.ID != request.ApprovalID || recovered.Identity != request.Target || string(recovered.Content) != string(detail.Content) {
		t.Fatal("fresh HTTP client could not recover the original approved snapshot", recovered, err)
	}
	request = domain.ExecutionRequest{Target: recovered.Identity, ApprovalID: recovered.Approval.ID}
	answer, err := c.AnswerQuestion(ctx, first.Target.Answer("approved HTTP answer"))
	if err != nil || answer.Effect != domain.AnswerRecorded || answer.Delivery != started.Delivery {
		t.Fatal("native HTTP answer", answer, err)
	}
	if _, err := c.AnswerQuestion(ctx, first.Target.Answer("stale answer")); err == nil {
		t.Fatal("stale HTTP answer accepted")
	}
	resumed, err := c.ResumeExecution(ctx, domain.ResumeRequest{Target: request.Target, ApprovalID: request.ApprovalID, Delivery: started.Delivery, OpenedDigest: started.OpenedDigest})
	if err != nil || resumed.Delivery != started.Delivery || resumed.OpenedDigest != started.OpenedDigest {
		t.Fatal("native HTTP resume", resumed, err)
	}
	if next := question(first.Target.Execution); next.Target.OpenedDigest != first.Target.OpenedDigest || next.Target.Delivery != first.Target.Delivery {
		t.Fatal("HTTP resume replaced native identity", next)
	}
}

func nativeJourneyProviderScript() string {
	return `#!/bin/sh
if [ -n "$OPENAI_API_KEY" ] || [ -n "$ANTHROPIC_API_KEY" ]; then exit 90; fi
case "$1" in
--version) printf 'codex-cli 0.160.0\n';exit 0;;
debug) printf '{"models":[{"slug":"qa-native"}]}\n';exit 0;;
doctor|plugin) printf '{}\n';exit 0;;
esac
if [ "$#" -eq 3 ] && [ "$1" = "--model" ] && [ "$3" = '` + "Run the shell command `git rev-parse HEAD` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command'\"'\"'s output, and stop." + `' ]; then
  sha=$(git rev-parse HEAD) || exit 1
  printf 'BATUTA-CAPABLE %s\n' "$sha"
  exit 0
fi
printf 'submission\n' >> "$HOME/native-launches"
printf 'BATUTA-QUESTION: approved HTTP answer?\n'
`
}

func TestNativeJourneyProviderCapability(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	t.Setenv("HOME", home)
	cli := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(cli, []byte(nativeJourneyProviderScript()), 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
	prompt := "Run the shell command `git rev-parse HEAD` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command's output, and stop."
	for range 2 {
		git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "next HEAD")
		c := exec.Command(cli, "--model", "qa-native", prompt)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil || string(out) != "BATUTA-CAPABLE "+git("rev-parse", "HEAD")+"\n" {
			t.Fatalf("capability: %s %v", out, err)
		}
		if git("status", "--porcelain") != "" {
			t.Fatal("probe changed tree")
		}
		if _, err := os.Stat(filepath.Join(home, "native-launches")); !os.IsNotExist(err) {
			t.Fatal("probe launched task", err)
		}
	}
	for _, args := range [][]string{{"--model", prompt, "task"}, {"--model", "qa-native", "prefix " + prompt}, {"--model", "qa-native", prompt, "extra"}, {"--model", "qa-native", "--effort", prompt, "task"}, {"--model", "qa-native", "task"}} {
		c := exec.Command(cli, args...)
		c.Dir = root
		out, err := c.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), "BATUTA-QUESTION:") || strings.Contains(string(out), "BATUTA-CAPABLE") {
			t.Fatalf("ordinary invocation %q: %s %v", args, out, err)
		}
	}
	c := exec.Command(cli, "--model", "qa-native", prompt)
	c.Dir = t.TempDir()
	out, err := c.CombinedOutput()
	if err == nil || strings.Contains(string(out), "BATUTA-CAPABLE") || !strings.Contains(string(out), "fatal:") {
		t.Fatalf("git failure hidden: %s %v", out, err)
	}
}
