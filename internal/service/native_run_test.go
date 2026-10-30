//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/routing"
)

func nativeRunFixture(t *testing.T) (*Service, domain.PlanApproval, string) {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	module := exec.Command(goPath, "list", "-m", "-f", "{{.Dir}}", "github.com/batuta-ai/core")
	raw, err := module.Output()
	if err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(t.TempDir(), "batuta")
	build := exec.Command(goPath, "build", "-ldflags", "-X main.buildVersion=v1.1.0-beta.55.2", "-o", core, "./cmd/batuta")
	build.Dir = strings.TrimSpace(string(raw))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("public Core build: %s %v", output, err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root, bin := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(bin, "codex")
	write(cli, `#!/bin/sh
if [ -n "$OPENAI_API_KEY" ] || [ -n "$ANTHROPIC_API_KEY" ]; then exit 90; fi
case "$1" in
--version) printf 'codex-cli 0.160.0\n';exit 0;;
debug) printf '{"models":[{"slug":"qa-native"}]}\n';exit 0;;
doctor|plugin) printf '{}\n';exit 0;;
--verify) printf 'TASK 1: DONE\n';exit 0;;
esac
if [ "$1" = "--model" ] && { [ "$#" -eq 3 ] || { [ "$#" -eq 5 ] && [ "$3" = "--effort" ]; }; }; then
argument=$3
if [ "$#" -eq 5 ]; then argument=$5; fi
if [ "$argument" = "Run the shell command `+"\\`git rev-parse HEAD\\`"+` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command's output, and stop." ]; then
  sha=$(git rev-parse HEAD) || exit 1
  printf 'BATUTA-CAPABLE %s\n' "$sha"
  exit 0
fi
fi
printf 'submission\n' >> "$HOME/native-launches"
printf 'BATUTA-QUESTION: which approved output?\n'
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(git)+string(os.PathListSeparator)+"/bin")
	t.Setenv("OPENAI_API_KEY", "ambient-should-never-cross")
	skills := installedSkillFixture(t, t.TempDir(), "batuta", "batuta-loop", "batuta-review")
	t.Setenv("BATUTA_SKILLS", skills)
	write(filepath.Join(skills, "adapters/codex.md"), "---\nname: codex\nexecutable: codex\nrun: 'codex --model {model} \"{brief}\" < /dev/null'\nreadonly: 'codex --verify --model {model} \"{prompt}\" < /dev/null'\navailable: 'codex --version'\nmodels: 'codex debug models'\nmodel_flags: --model {model}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n")
	write(filepath.Join(skills, "templates/generic.md"), "# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n")
	write(filepath.Join(root, ".batuta/profile.md"), "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n")
	write(filepath.Join(root, ".batuta/routing.md"), "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | qa-native |\n| medium | * | codex | qa-native |\n| high | * | codex | qa-native |\n| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | codex | qa-native |\n")
	write(filepath.Join(root, ".batuta/plans/alpha.md"), "# Plan — Alpha\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n")
	write(filepath.Join(root, ".gitignore"), ".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n")
	planBytes, err := os.ReadFile(filepath.Join(root, ".batuta/plans/alpha.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routing.ParsePlan("alpha", planBytes); err != nil {
		t.Fatalf("invalid native fixture plan: %v", err)
	}
	for _, argv := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "console-native-qa"}, {"config", "user.email", "qa@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "chore: native fixture"}} {
		command := exec.Command(git, argv...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
	}
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st, session.New(t.TempDir()), nil)
	svc.Home = home
	svc.CoreExecutable = core
	account := domain.Account{ID: "acct_native", Adapter: "codex", Connected: true, SessionCap: 1}
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	authenticateFixtureAccount(t, svc, account, "native-account")
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	squad := domain.Squad{ID: "q", Members: []domain.Member{{ID: "member", CLI: "codex", Model: "qa-native", AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}}
	if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", SquadID: "q", SquadSnapshot: squad, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: ".batuta/plans/alpha.md"}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
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
	return svc, a, root
}

func waitNativeQuestionAndIdle(t *testing.T, ctx context.Context, svc *Service, a domain.PlanApproval, delivery string, afterExecution int) domain.PendingQuestion {
	t.Helper()
	for {
		state, err := svc.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Active && state.Watch != nil && state.Watch.PendingQuestion != nil && state.Watch.PendingQuestion.Target.Execution > afterExecution {
			return *state.Watch.PendingQuestion
		}
		select {
		case <-ctx.Done():
			t.Fatal("native question/idle state missing", state)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestNativeAnswerAndResumeKeepOriginalReceiptAfterSourceDeletion(t *testing.T) {
	svc, a, root := nativeRunFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
	if err != nil {
		t.Fatal(started, err)
	}
	question := waitNativeQuestionAndIdle(t, ctx, svc, a, started.Delivery, 0)
	if err := os.Remove(filepath.Join(root, a.Identity.Binding.PlanPath)); err != nil {
		t.Fatal(err)
	}
	answered, err := svc.AnswerQuestion(ctx, question.Target.Answer("approved answer"))
	if err != nil || answered.Effect != domain.AnswerRecorded || answered.Delivery != started.Delivery {
		t.Fatal("native answer after deletion", answered, err)
	}
	if _, err := svc.AnswerQuestion(ctx, question.Target.Answer("replayed")); err == nil {
		t.Fatal("stale native question answered")
	}
	resumed, err := svc.ResumeExecution(ctx, domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: started.Delivery, OpenedDigest: started.OpenedDigest})
	if err != nil || resumed.Effect != domain.ExecutionStarted || resumed.Delivery != started.Delivery || resumed.OpenedDigest != started.OpenedDigest {
		t.Fatal("native exact resume", resumed, err)
	}
	next := waitNativeQuestionAndIdle(t, ctx, svc, a, started.Delivery, question.Target.Execution)
	if next.Target.OpenedDigest != question.Target.OpenedDigest || next.Target.Delivery != question.Target.Delivery {
		t.Fatal("resume replaced native opening", next)
	}
	js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	records, err := js.Read(started.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	openings := 0
	for _, record := range records {
		if record.Kind == loop.KindOpened {
			openings++
		}
	}
	if openings != 1 {
		t.Fatal("resume duplicated native opening", openings)
	}
}

func TestNativeRunStartsExactApprovedDeliveryAndParksQuestion(t *testing.T) {
	svc, a, root := nativeRunFixture(t)
	before, err := os.ReadFile(filepath.Join(root, a.Identity.Binding.PlanPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
	if err != nil || result.Effect != domain.ExecutionStarted || result.Delivery == "" || result.OpenedDigest == "" {
		t.Fatal("approved native start", result, err)
	}
	for {
		view, err := svc.WatchMission("m", "w", result.Delivery)
		if err != nil {
			t.Fatal(err)
		}
		if view.PendingQuestion != nil {
			if view.PendingQuestion.Target.MissionID != "m" || view.PendingQuestion.Target.OpenedDigest != result.OpenedDigest {
				t.Fatal(view.PendingQuestion)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native question did not arrive", view)
		case <-time.After(10 * time.Millisecond):
		}
	}
	after, err := os.ReadFile(filepath.Join(root, a.Identity.Binding.PlanPath))
	if err != nil || string(before) != string(after) {
		t.Fatal("native start changed approved source", err)
	}
	if _, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}); err == nil {
		t.Fatal("native delivery was duplicated")
	}
}

type nativeVersionCapabilities struct{ version string }

func (r nativeVersionCapabilities) Run(context.Context, publication.Command) (publication.CommandResult, error) {
	return publication.CommandResult{Stdout: []byte(`{"version":"` + r.version + `","commands":["loop","review","inventory"]}`)}, nil
}

func TestNativeCoreRequiresPublishedBeta55_2(t *testing.T) {
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{CoreExecutable: image}
	for _, version := range []string{"v1.1.0-beta.55.2", "1.1.0-beta.55.2", "v1.1.0-beta.55.1", "1.1.0-beta.55.1", "v1.1.0-beta.55", "1.1.0-beta.55", "v1.1.0-beta.49", "v1.1.0-beta.54", "v1.1.0-beta.56", "v1.1.0"} {
		t.Run(version, func(t *testing.T) {
			got, err := svc.qualifiedNativeCore(context.Background(), nativeVersionCapabilities{version: version})
			want := version == "v1.1.0-beta.55.2" || version == "1.1.0-beta.55.2"
			if want {
				if err != nil || got != image {
					t.Fatalf("published CLI rejected: %q %v", got, err)
				}
			} else if got != "" || !errors.Is(err, domain.ErrExecutionUnavailable) {
				t.Fatalf("unqualified CLI accepted: %q %v", got, err)
			}
		})
	}
}
