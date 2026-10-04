package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/store"
)

func commandFixture(t *testing.T, adapter string) (admissionFixture, string, string) {
	t.Helper()
	f := admissionSetup(t, 3)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.svc.Session.Shutdown(ctx); err != nil {
			t.Errorf("drain: %v", err)
		}
	})
	cliDir := filepath.Dir(f.launches)
	if err := os.Remove(filepath.Join(cliDir, "codex")); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(cliDir, adapter)
	out := filepath.Join(cliDir, "argv")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	banner := "codex-cli 0.160.0"
	if adapter == "claude" {
		banner = "2.1.289 (Claude Code)"
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + banner + "'; exit 0; fi\n[ -t 0 ] && [ -t 1 ] || exit 91\nprintf '%s\\0' \"$0\" \"$@\" > " + quote(out) + "\nwhile IFS= read -r line; do printf 'ack:%s\\n' \"$line\"; done\n"
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.account.Adapter = adapter
	if err := f.svc.Store.PutAccount(f.account); err != nil {
		t.Fatal(err)
	}
	authenticateFixtureAccount(t, f.svc, f.account, "A")
	return f, cli, out
}

func observedCommand(t *testing.T, f admissionFixture, sess domain.Session, out string, want []string) {
	t.Helper()
	readyProcess(t, f.svc, sess)
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if !slices.Equal(got, want) || !slices.Equal(sess.Argv, want) {
		t.Errorf("received=%q Session.Argv=%q want=%q", got, sess.Argv, want)
	}
}

func commandEvent(t *testing.T, st *store.Store, sess domain.Session, kind string, want map[string]any) {
	t.Helper()
	events, err := st.EventsSince(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == kind && (e.Payload["id"] == sess.ID || e.Payload["session"] == sess.ID) {
			if !reflect.DeepEqual(e.Payload["command"], want) {
				t.Errorf("command report=%#v want=%#v", e.Payload["command"], want)
			}
			return
		}
	}
	t.Errorf("missing %s event for %s", kind, sess.ID)
}

func wantCommandReport(mode, goalSource, model, effort string) map[string]any {
	setting := func(value string) any {
		source := "explicit"
		if value == "" {
			source = "cli_default"
		}
		if mode == "custom" {
			source = "unknown"
		}
		result := map[string]any{"source": source}
		if value != "" {
			result["value"] = value
		}
		return result
	}
	return map[string]any{"mode": mode, "goal_source": goalSource, "model": setting(model), "effort": setting(effort)}
}

func TestOfficialSessionCommand(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, binding := range []string{"snapshot", "same-ID", "mixed", "different-ID", "defaults", "blank", "whitespace", "whitespace-generic", "injected", "custom"} {
			t.Run(adapter+"/"+binding, func(t *testing.T) {
				f, cli, out := commandFixture(t, adapter)
				goal := "  literal goal; $(do-not-run) `literal`\nsecond line  "
				model, effort := "future-model;$(literal)", "high"
				snap := domain.Member{ID: "member", CLI: adapter, Maestro: true}
				current := domain.Member{ID: "member", CLI: adapter, Model: "current-model", Effort: "low"}
				switch binding {
				case "snapshot", "injected":
					snap.Model, snap.Effort = model, effort
				case "same-ID":
					current.Model, current.Effort = model, effort
				case "mixed":
					snap.Model = model
					current.Effort = effort
				case "different-ID":
					current.ID = "replacement"
					model, effort = "", ""
				default:
					current.Model, current.Effort = "", ""
					model, effort = "", ""
				}
				if binding == "blank" {
					goal = ""
				}
				if binding == "whitespace" || binding == "whitespace-generic" {
					goal = " \t\n "
				}
				f.mission.Brief = goal
				f.mission.SquadSnapshot.Members = []domain.Member{snap}
				if err := f.svc.Store.PutMission(f.mission); err != nil {
					t.Fatal(err)
				}
				if err := f.svc.Store.PutSquad(domain.Squad{ID: f.mission.SquadID, Members: []domain.Member{current}}); err != nil {
					t.Fatal(err)
				}
				inject := binding == "injected" || binding == "whitespace"
				in := StartSessionIn{MissionID: f.mission.ID, AccountID: f.account.ID, Inject: &inject}
				mode, goalSource := "interactive", "prompt"
				want := []string{cli}
				if model != "" {
					want = append(want, "--model", model)
				}
				if effort != "" {
					if adapter == "codex" {
						want = append(want, "--config", `model_reasoning_effort="high"`)
					} else {
						want = append(want, "--effort", effort)
					}
				}
				if binding == "injected" {
					goal = "Use batuta-plan for this mission. Do not implement yet. Write .batuta/plans/<slug>.md with tasks, scope and acceptance criteria, set Status to proposed, and wait for approval.\n\nGoal:\n" + strings.TrimSpace(goal)
				}
				if strings.TrimSpace(goal) != "" {
					want = append(want, goal)
				} else {
					goalSource = "none"
				}
				if binding == "custom" {
					in.Argv = []string{cli, "literal supplied goal", "--custom-argument"}
					want = slices.Clone(in.Argv)
					mode, goalSource = "custom", "custom_argv"
				}
				sess, err := f.svc.StartSession(in)
				if err != nil {
					t.Fatal(err)
				}
				observedCommand(t, f, sess, out, want)
				report := wantCommandReport(mode, goalSource, model, effort)
				commandEvent(t, f.svc.Store, sess, "session.started", report)
				if binding == "custom" {
					in.Argv[1] = "caller mutation"
					if !slices.Equal(sess.Argv, want) {
						t.Error("session aliases supplied argv")
					}
				}
				if _, err := f.svc.StopSession(sess.ID); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := f.svc.Session.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
				var seq int
				var name, path string
				if err := f.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
					t.Fatal(err)
				}
				if err := f.svc.Store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				stored, err := reopened.GetSession(sess.ID)
				if err != nil || !slices.Equal(stored.Argv, want) {
					t.Errorf("reopened argv=%q want=%q err=%v", stored.Argv, want, err)
				}
				commandEvent(t, reopened, sess, "session.started", report)
			})
		}
	}
}

func TestOfficialLoginInitCommand(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"login", "init"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				f, cli, out := commandFixture(t, adapter)
				want := []string{cli}
				kind, goalSource := "account.login", "none"
				var sess domain.Session
				var err error
				if mode == "login" {
					if adapter == "codex" {
						want = append(want, "login")
					} else {
						want = append(want, "auth", "login")
					}
					sess, err = f.svc.StartLogin(f.account.ID, "web", "test")
				} else {
					kind, goalSource = "workspace.init", "prompt"
					want = append(want, "Run batuta-init for this project. Write .batuta/profile.md and .batuta/routing.md from these answers, then stop.\n\nsetup literal answers")
					sess, err = f.svc.StartInit(f.workspace.ID, "setup literal answers", "web", "test")
				}
				if err != nil {
					t.Fatal(err)
				}
				if sess.Adapter != adapter {
					t.Errorf("adapter=%q want=%q", sess.Adapter, adapter)
				}
				observedCommand(t, f, sess, out, want)
				commandEvent(t, f.svc.Store, sess, kind, wantCommandReport(mode, goalSource, "", ""))
			})
		}
	}
}

func TestOfficialMissingCLIHasNoLaunch(t *testing.T) {
	for _, mode := range []string{"session", "login", "init"} {
		t.Run(mode, func(t *testing.T) {
			f, cli, out := commandFixture(t, "codex")
			if err := os.Remove(cli); err != nil {
				t.Fatal(err)
			}
			var sess domain.Session
			var err error
			switch mode {
			case "login":
				sess, err = f.svc.StartLogin(f.account.ID, "web", "test")
			case "init":
				sess, err = f.svc.StartInit(f.workspace.ID, "answers", "web", "test")
			default:
				sess, err = f.start()
			}
			if sess.PID != 0 {
				if _, stopErr := f.svc.StopSession(sess.ID); stopErr != nil {
					t.Error(stopErr)
				}
			}
			var lookup *exec.Error
			if !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &lookup) {
				t.Errorf("lookup cause lost: %v", err)
			}
			assertNoCommandLaunch(t, f, out)
		})
	}
}

func assertNoCommandLaunch(t *testing.T, f admissionFixture, out string) {
	t.Helper()
	sessions, err := f.svc.Store.ListSessions()
	if err != nil || len(sessions) != 0 {
		t.Errorf("unexpected sessions=%v err=%v", sessions, err)
	}
	events, err := f.svc.Store.EventsSince(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "session.started" || event.Kind == "account.login" || event.Kind == "workspace.init" {
			t.Errorf("unexpected start event: %v", event)
		}
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected child artifact: %v", err)
	}
}

func TestOfficialRejectsConfigurationBeforeSpawn(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, failure := range []string{"effort", "model", "goal", "reserved-goal", "custom-model", "custom-effort"} {
			t.Run(adapter+"/"+failure, func(t *testing.T) {
				f, cli, out := commandFixture(t, adapter)
				snap := domain.Member{ID: "member", CLI: adapter}
				off := false
				in := StartSessionIn{MissionID: f.mission.ID, Inject: &off}
				switch failure {
				case "effort":
					snap.Effort = "default"
				case "model":
					snap.Model = "--model=other"
				case "goal":
					f.mission.Brief = "--help"
				case "reserved-goal":
					f.mission.Brief = "update"
				case "custom-model":
					snap.Model = "future-model"
					in.Argv = []string{cli}
				case "custom-effort":
					snap.Effort = "high"
					in.Argv = []string{cli}
				}
				f.mission.SquadSnapshot.Members = []domain.Member{snap}
				if err := f.svc.Store.PutMission(f.mission); err != nil {
					t.Fatal(err)
				}
				sess, err := f.svc.StartSession(in)
				if !errors.Is(err, adapters.ErrInvalidCommand) {
					t.Errorf("invalid configuration classification: %v", err)
				}
				if sess.PID != 0 {
					if _, err := f.svc.StopSession(sess.ID); err != nil {
						t.Error(err)
					}
				}
				assertNoCommandLaunch(t, f, out)
			})
		}
	}
}

func TestOfficialUnsupportedAdaptersDoNotLaunch(t *testing.T) {
	for _, adapter := range []string{"cursor-agent", "opencode", "gemini"} {
		for _, mode := range []string{"session", "custom", "login", "init"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				f, cli, out := commandFixture(t, adapter)
				var sess domain.Session
				var err error
				switch mode {
				case "login":
					sess, err = f.svc.StartLogin(f.account.ID, "web", "test")
				case "init":
					sess, err = f.svc.StartInit(f.workspace.ID, "answers", "web", "test")
				default:
					off := false
					in := StartSessionIn{MissionID: f.mission.ID, Adapter: adapter, Inject: &off}
					if mode == "custom" {
						in.Argv = []string{cli, "custom"}
					}
					sess, err = f.svc.StartSession(in)
				}
				if mode == "init" {
					if code, ok := ReadinessFailure(err); !ok || code != "authenticated_account_required" {
						t.Errorf("init without a supported authenticated account: %v", err)
					}
				} else if !errors.Is(err, adapters.ErrUnsupportedCommand) {
					t.Errorf("unsupported command classification: %v", err)
				}
				if sess.PID != 0 {
					if _, err := f.svc.StopSession(sess.ID); err != nil {
						t.Error(err)
					}
				}
				assertNoCommandLaunch(t, f, out)
			})
		}
	}
}

func TestQualifiedClaudeRejectsPurgeSubcommand(t *testing.T) {
	f, cli, out := commandFixture(t, "claude")
	script, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte(strings.ReplaceAll(string(script), "2.1.286", "2.1.289")), 0700); err != nil {
		t.Fatal(err)
	}
	f.mission.Brief = "purge"
	if err := f.svc.Store.PutMission(f.mission); err != nil {
		t.Fatal(err)
	}
	off := false
	live, err := f.svc.StartSession(StartSessionIn{MissionID: f.mission.ID, AccountID: f.account.ID, Adapter: "claude", Inject: &off})
	if live.PID != 0 {
		f.svc.StopSession(live.ID)
	}
	if !errors.Is(err, adapters.ErrInvalidCommand) {
		t.Errorf("Claude289 destructive subcommand accepted as goal: %v", err)
	}
	assertNoCommandLaunch(t, f, out)
}
