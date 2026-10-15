//go:build darwin || linux

package tui

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestJourneyPublicCoreQuestionResumeAndRestart(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	core := filepath.Join(t.TempDir(), "batuta")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-ldflags", "-X main.buildVersion=v1.1.0-beta.55", "-o", core, "github.com/batuta-ai/core/cmd/batuta")
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("frozen public Core build: %s %v", raw, err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root, home, bin := t.TempDir(), t.TempDir(), t.TempDir()
			write := func(path, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			nativeModel := map[string]string{"codex": "qa-native", "claude": "sonnet"}[provider]
			version := map[string]string{"codex": "codex-cli 0.160.0", "claude": "2.1.289 (Claude Code)"}[provider]
			write(filepath.Join(bin, provider), nativeJourneyProviderScript(version))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(git)+string(os.PathListSeparator)+"/bin")
			skills := filepath.Join(home, "skills")
			for _, name := range []string{"batuta", "batuta-loop", "batuta-review"} {
				write(filepath.Join(skills, name, "SKILL.md"), "---\nname: "+name+"\ndescription: disposable fixture\n---\n")
			}
			write(filepath.Join(skills, "batuta/adapters", provider+".md"), "---\nname: "+provider+"\nexecutable: "+provider+"\nrun: '"+provider+" --model {model} \"{brief}\" < /dev/null'\nreadonly: '"+provider+" --verify --model {model} \"{prompt}\" < /dev/null'\navailable: '"+provider+" --version'\nmodels: '"+provider+" debug models'\nmodel_flags: --model {model}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n")
			write(filepath.Join(skills, "batuta/templates/generic.md"), "# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n")
			t.Setenv("BATUTA_SKILLS", filepath.Join(skills, "batuta"))
			write(filepath.Join(root, ".batuta/profile.md"), "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n")
			write(filepath.Join(root, ".batuta/routing.md"), "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | "+provider+" | "+nativeModel+" |\n| medium | * | "+provider+" | "+nativeModel+" |\n| high | * | "+provider+" | "+nativeModel+" |\n| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | "+provider+" | "+nativeModel+" |\n")
			planPath := ".batuta/plans/alpha.md"
			write(filepath.Join(root, planPath), "# Plan — Alpha\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n")
			write(filepath.Join(root, ".gitignore"), ".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n")
			for _, argv := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "fixture"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "chore: disposable native fixture"}} {
				cmd := exec.Command(git, argv...)
				cmd.Dir = root
				if raw, err := cmd.CombinedOutput(); err != nil {
					t.Fatal(string(raw), err)
				}
			}
			dbPath := filepath.Join(home, "catalog.db")
			st, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			svc := service.New(st, session.New(t.TempDir()), nil)
			svc.Home = home
			svc.CoreExecutable = core
			closeService := func(s *service.Service) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := s.ShutdownNative(ctx); err != nil {
					t.Error(err)
				}
				if err := s.Session.Shutdown(ctx); err != nil {
					t.Error(err)
				}
				if err := s.Store.Close(); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { closeService(svc) })
			a, err := svc.CreateAccount("native", provider)
			if err != nil {
				t.Fatal(err)
			}
			a, err = svc.ConnectAccount(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			journeyFixtureAuthentication(t, svc, a)
			ws, err := svc.CreateWorkspace("native", root)
			if err != nil {
				t.Fatal(err)
			}
			sq, err := svc.SaveSquad(domain.Squad{Name: "native", Members: []domain.Member{{Role: "lead", CLI: provider, Model: nativeModel, AccountID: a.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}})
			if err != nil {
				t.Fatal(err)
			}
			first, err := svc.CreateMission(ws.ID, "other mission", "", sq.ID)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := svc.CreateMission(ws.ID, "selected mission", "", sq.ID)
			if err != nil {
				t.Fatal(err)
			}
			handler := (&api.Server{Svc: svc, Token: "disposable-account-fixture"}).Handler()
			var mutations []string
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" || r.Method == "PUT" {
					mutations = append(mutations, r.URL.Path)
				}
				handler.ServeHTTP(w, r)
			})
			m := model{api: c, missions: []domain.Mission{first, selected}, screen: scrMissions, cursor: 1}
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "b")
			m = catalogPaste(m, planPath)
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "o")
			m = journeyInspectAll(t, m)
			m = journeyKey(t, m, "y")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "e")
			m = journeyKey(t, m, "enter")
			original := m.journeyRecord()
			if original.state == nil || original.state.OpenedDigest == "" {
				t.Fatal("native start failed", m.View().Content)
			}
			waitQuestion := func(after int) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				for {
					m = journeyFinish(t, m, m.readJourney("refresh"))
					r := m.journeyRecord()
					if r.fresh && r.state != nil && !r.state.Active && r.state.Watch != nil && r.state.Watch.PendingQuestion != nil && r.state.Watch.PendingQuestion.Target.Execution > after {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatal("native question did not arrive", m.View().Content)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			waitQuestion(0)
			firstQuestion := m.journeyRecord().state.Watch.PendingQuestion.Target
			// Preserve the source artifact; external deletion is simulated by moving it.
			if err := os.Rename(filepath.Join(root, planPath), filepath.Join(home, "preserved-original-plan.md")); err != nil {
				t.Fatal(err)
			}
			closeService(svc)
			st, err = store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			svc = service.New(st, session.New(t.TempDir()), nil)
			svc.Home = home
			svc.CoreExecutable = core
			journeyFixtureAuthentication(t, svc, a)
			handler = (&api.Server{Svc: svc, Token: "disposable-account-fixture"}).Handler()
			m = journeyKey(t, m, "o")
			accountText(t, m, "Original approved snapshot")
			m = journeyKey(t, m, "esc")
			m = journeyKey(t, m, "f")
			if r := m.journeyRecord(); r.receipt.ID != original.receipt.ID || r.receipt.Identity != original.receipt.Identity || r.state.OpenedDigest != original.state.OpenedDigest || r.state.Watch.PendingQuestion.Target != firstQuestion {
				t.Fatal("restart/source loss changed authority")
			}
			m = journeyKey(t, m, "j")
			m = catalogPaste(m, "exact native\nanswer")
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "enter")
			if m.journeyRecord().effect != "recorded" {
				t.Fatal("native answer failed", m.View().Content)
			}
			if len(mutations) != 4 {
				t.Fatal("automatic resume", mutations)
			}
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "g")
			m = journeyKey(t, m, "enter")
			waitQuestion(firstQuestion.Execution)
			r := m.journeyRecord()
			if r.state.Delivery != original.state.Delivery || r.state.OpenedDigest != original.state.OpenedDigest || r.receipt.Identity != original.receipt.Identity || len(mutations) != 5 {
				t.Fatal("native resume replaced original delivery", mutations)
			}
			other, err := svc.Store.GetMission(first.ID)
			if err != nil || other.Binding != nil {
				t.Fatal("foreign mission bound", other, err)
			}
			js, err := journal.Open(r.receipt.Identity.Binding.CanonicalRoot)
			if err != nil {
				t.Fatal(err)
			}
			records, err := js.Read(r.state.Delivery)
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
				t.Fatal("duplicate native opening", openings)
			}
			if !strings.Contains(m.View().Content, firstQuestion.OpenedDigest) {
				t.Fatal("original opening not displayed")
			}
		})
	}
}

func nativeJourneyProviderScript(version string) string {
	return "#!/bin/sh\nif [ -n \"$OPENAI_API_KEY\" ] || [ -n \"$ANTHROPIC_API_KEY\" ]; then exit 90; fi\ncase \"$1\" in\n--version) printf '%s\\n' '" + version + "';exit 0;;\ndebug) printf '{\"models\":[{\"slug\":\"qa-native\"}]}\\n';exit 0;;\ndoctor|plugin) printf '{}\\n';exit 0;;\n--verify) printf 'TASK 1: DONE\\n';exit 0;;\nesac\nif [ \"$#\" -eq 3 ] && [ \"$1\" = \"--model\" ] && [ \"$3\" = 'Run the shell command `git rev-parse HEAD` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command'\"'\"'s output, and stop.' ]; then\n  sha=$(git rev-parse HEAD) || exit 1\n  printf 'BATUTA-CAPABLE %s\\n' \"$sha\"\n  exit 0\nfi\nprintf 'BATUTA-QUESTION: exact native question?\\n'\n"
}

func TestNativeJourneyProviderCapability(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	cli := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(cli, []byte(nativeJourneyProviderScript("fixture-version")), 0700); err != nil {
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
