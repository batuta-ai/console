//go:build darwin || linux

package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeClaudeUsesSelectedAccountAndPublicDeclaredModel(t *testing.T) {
	svc, approval, root := nativeRunFixture(t)
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(codex)
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(filepath.Dir(codex), "claude")
	if err := os.WriteFile(claude, []byte(strings.ReplaceAll(string(bytes), "codex-cli 0.160.0", "2.1.289 (Claude Code)")), 0700); err != nil {
		t.Fatal(err)
	}
	skills := os.Getenv("BATUTA_SKILLS")
	bytes, err = os.ReadFile(filepath.Join(skills, "adapters/codex.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "adapters/claude.md"), []byte(strings.ReplaceAll(string(bytes), "codex", "claude")), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".batuta/routing.md")
	bytes, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	routing := strings.NewReplacer("codex", "claude", "qa-native", "sonnet").Replace(string(bytes))
	if err := os.WriteFile(path, []byte(routing), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".batuta/routing.md"}, {"commit", "-q", "-m", "chore: select synthetic Claude"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
	}
	account := domain.Account{ID: "acct_claude", Adapter: "claude", Connected: true, SessionCap: 1}
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	authenticateFixtureAccount(t, svc, account, "selected-claude")
	mission, err := svc.Store.GetMission("m")
	if err != nil {
		t.Fatal(err)
	}
	member := &mission.SquadSnapshot.Members[0]
	member.CLI, member.Model, member.AccountID = "claude", "sonnet", account.ID
	if err := svc.Store.PutMission(mission); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
	if err != nil {
		t.Fatal("native Claude start", started, err)
	}
	question := waitNativeQuestionAndIdle(t, ctx, svc, approval, started.Delivery, 0)
	if _, err := svc.AnswerQuestion(ctx, question.Target.Answer("selected Claude answer")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeExecution(ctx, domain.ResumeRequest{Target: approval.Identity, ApprovalID: approval.ID, Delivery: started.Delivery, OpenedDigest: started.OpenedDigest}); err != nil {
		t.Fatal(err)
	}
	waitNativeQuestionAndIdle(t, ctx, svc, approval, started.Delivery, question.Target.Execution)
	ad, _ := adapters.Get("claude")
	launches, err := os.ReadFile(filepath.Join(ad.IsolateDir(svc.Home, account.ID), "native-launches"))
	if err != nil || string(launches) != "submission\nsubmission\n" {
		t.Fatal("native Claude account submissions", string(launches), err)
	}
	codexAdapter, _ := adapters.Get("codex")
	if _, err := os.Stat(filepath.Join(codexAdapter.IsolateDir(svc.Home, "acct_native"), "native-launches")); !os.IsNotExist(err) {
		t.Fatal("unselected Codex account was used", err)
	}
}
