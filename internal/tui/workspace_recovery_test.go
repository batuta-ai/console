package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/domain"
)

func TestWorkspaceInitUncertainIdentity(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, failure := range []string{"400", "409", "500", "malformed", "lost", "workspace", "account", "adapter", "work", "unknown", "empty"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			ws := []domain.Workspace{{ID: "chosen", Root: "/chosen", Setup: domain.WorkspaceSetup{State: "partial"}}}
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					calls.Add(1)
					out := domain.Session{ID: "possible-init", WorkspaceID: "chosen", AccountID: "account", Adapter: "codex", Work: "init", Process: "running"}
					switch failure {
					case "400":
						http.Error(w, `{"error":"invalid"}`, 400)
						return
					case "409":
						http.Error(w, `{"error":"init_conflict"}`, 409)
						return
					case "500":
						http.Error(w, `{"error":"failed"}`, 500)
						return
					case "malformed":
						io.WriteString(w, `{"id":"possible-init",`)
						return
					case "lost":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					case "workspace":
						out.WorkspaceID = "foreign"
					case "account":
						out.AccountID = "foreign"
					case "adapter":
						out.Adapter = "claude"
					case "work":
						out.Work = "login"
					case "unknown":
						out.Process = "unknown"
					case "empty":
						out.ID = ""
					}
					_ = json.NewEncoder(w).Encode(out)
					return
				}
				workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("account", "codex")}, ws)
			})
			m := accountModel(t, c)
			m.ws = ws
			m.sessID = "unrelated"
			m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "keep notes"))
			m, cmd := workspaceKey(m, "ctrl+enter")
			m = accountComplete(t, m, cmd)
			if m.workspace.uncertain == nil || m.workspace.selected.ID != "chosen" || m.sessID != "unrelated" || m.link != nil {
				t.Fatal("uncertain init lost identity or attached")
			}
			accountText(t, m, "/chosen")
			accountText(t, m, "keep notes")
			for _, key := range []string{"ctrl+enter", "ctrl+r", "ctrl+enter"} {
				m, cmd = workspaceKey(m, key)
				if cmd != nil {
					t.Fatal("uncertain init replayed")
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			if m.workspace.draft.reviewing {
				t.Fatal("uncertain result retained confirmation")
			}
			m, _ = workspaceKey(m, "esc")
			m, _ = workspaceKey(m, "h")
			accountText(t, m, "Possible workspace effect")
		})
	}
}

func TestWorkspaceAccountSwitchEditsInvalidateConfirmation(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m := New(nil).(model)
	m.ws = []domain.Workspace{{ID: "one", Root: "/one"}, {ID: "two", Root: "/two"}}
	m.accounts = []domain.Account{workspaceReadyAccount("one-account", "codex"), workspaceReadyAccount("two-account", "claude")}
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = next.(model)
	if m.workspace.draft.reviewing || m.workspace.draft.account.ID != "two-account" || m.workspace.draft.config() != workspaceConfig() {
		t.Fatal("account switch reused confirmation or erased inputs")
	}
	m = workspaceReview(t, m)
	m, _ = workspaceKey(m, "tab")
	m, _ = workspaceKey(m, " plus")
	if m.workspace.draft.reviewing {
		t.Fatal("editing retained confirmation")
	}
	m, _ = workspaceKey(m, "ctrl+a")
	if m.screen != scrAccounts {
		t.Fatal("account workflow inaccessible")
	}
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "e")
	accountText(t, m, "Go TUI plus")
	m, _ = workspaceKey(m, "esc")
	m, _ = workspaceKey(m, "esc")
	m, _ = workspaceKey(m, "down")
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "e")
	if m.workspace.selected.ID != "two" || m.workspace.draft.config().Stack != "" || m.workspace.draft.account.ID != "" {
		t.Fatal("other workspace reused inputs/account")
	}
}

func TestWorkspaceManualSessionIdentityAndReady(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, process := range []string{"unknown", "exited", "running"} {
		t.Run(process, func(t *testing.T) {
			var mutations atomic.Int32
			sessions := []domain.Session{{ID: "foreign", WorkspaceID: "other", AccountID: "other", Adapter: "codex", Work: "init", Process: "running"}, {ID: "actual-init", WorkspaceID: "chosen", AccountID: "account", Adapter: "codex", Work: "init", Process: process, Owner: "web", OwnerClient: "owner"}}
			ws := []domain.Workspace{{ID: "chosen", Root: "/chosen", Setup: domain.WorkspaceSetup{State: "interrupted", Attempt: &domain.InitAttempt{SessionID: "actual-init", AccountID: "account", State: "interrupted", Process: process, StopRequested: true, RecoveryReason: "catalog_only"}}}}
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations.Add(1)
					t.Errorf("manual inspection mutated: %s", r.URL.Path)
				}
				if r.URL.Path == "/v0/sessions" {
					_ = json.NewEncoder(w).Encode(sessions)
					return
				}
				workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("account", "codex")}, ws)
			})
			m := accountModel(t, c)
			m.ws = ws
			m.sessID = "unrelated"
			m, _ = workspaceKey(m, "w")
			m, _ = workspaceKey(m, "enter")
			m, cmd := workspaceKey(m, "f")
			m = accountComplete(t, m, cmd)
			accountText(t, m, "catalog_only")
			m, _ = workspaceKey(m, "]")
			m, cmd = workspaceKey(m, "v")
			if process != "running" {
				if cmd != nil || m.sessID != "unrelated" {
					t.Fatal("unknown process attached")
				}
				return
			}
			if cmd == nil {
				t.Fatal("manual view unavailable")
			}
			next, attach := m.Update(cmd())
			m = next.(model)
			if m.sessID != "actual-init" || attach == nil || m.termMode || mutations.Load() != 0 {
				t.Fatal("manual view did not retain exact identity")
			}
		})
	}
}

func TestWorkspaceLocalizedViewsAndAttemptFacts(t *testing.T) {
	for _, lang := range []string{"pt", "en"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			labels := map[string][2]string{"missing": {"Ausente", "Missing"}, "partial": {"Parcial", "Partial"}, "running": {"Em execução", "Running"}, "interrupted": {"Interrompido", "Interrupted"}, "cancelled": {"Cancelado", "Cancelled"}, "ready": {"Pronto", "Ready"}, "invalid_workspace": {"Workspace inválido", "Invalid workspace"}}
			for state, labels := range labels {
				m := New(nil).(model)
				m.ws = []domain.Workspace{{ID: "actual-ws", Root: "/root", Setup: domain.WorkspaceSetup{State: state, Ready: state == "ready", Code: "actual_code", ConfigurationCode: "profile_missing", Attempt: &domain.InitAttempt{SessionID: "actual-session", AccountID: "actual-account", State: "exited_incomplete", Process: "exited", StopRequested: true, RecoveryReason: "actual_reason"}}}}
				m, _ = workspaceKey(m, "w")
				m, _ = workspaceKey(m, "enter")
				for _, want := range []string{"actual-ws", "actual-session", "actual-account", "actual_code", "actual_reason", "profile_missing"} {
					accountText(t, m, want)
				}
				index := 0
				if lang == "en" {
					index = 1
				}
				accountText(t, m, labels[index])
				m, _ = workspaceKey(m, "e")
				if state == "ready" && m.workspace.draft.editing {
					t.Fatal("ready opened duplicate init")
				}
				if state != "ready" && !strings.Contains(m.View().Content, "Stack") {
					t.Fatal("setup form unavailable")
				}
			}
		})
	}
}

func TestWorkspaceOversizedEditCannotConfirmOldValue(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var calls atomic.Int32
	ws := []domain.Workspace{{ID: "one", Root: "/one"}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls.Add(1)
			http.Error(w, `{}`, 500)
			return
		}
		workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("account", "codex")}, ws)
	})
	m := accountModel(t, c)
	m.ws = ws
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	m, _ = workspaceKey(m, "tab")
	next, _ := m.Update(tea.PasteMsg{Content: strings.Repeat("x", 9000)})
	m = next.(model)
	if m.workspace.draft.reviewing {
		t.Fatal("rejected oversized edit left old confirmation valid")
	}
	m, _ = workspaceKey(m, "ctrl+r")
	m, cmd := workspaceKey(m, "ctrl+enter")
	if cmd != nil {
		m = accountComplete(t, m, cmd)
	}
	if calls.Load() != 0 {
		t.Fatal("oversized edit submitted old values")
	}
	accountText(t, m, "Go TUI")
}

func TestWorkspacePendingViewCannotOverwriteNewEditing(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { accountCatalog(w, r, nil) })
	m := accountModel(t, c)
	m.ws = []domain.Workspace{{ID: "one", Root: "/one"}}
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "enter")
	m.workspace.sessions = []domain.Session{{ID: "s", WorkspaceID: "one", AccountID: "a", Adapter: "codex", Work: "init", Process: "running"}}
	m.accounts = []domain.Account{workspaceReadyAccount("a", "codex")}
	m, _ = workspaceKey(m, "]")
	m, _ = workspaceKey(m, "v")
	m, _ = workspaceKey(m, "e")
	if m.workspace.draft.editing {
		t.Fatal("pending view allowed a new editing state without invalidating the request")
	}
	m, cmd := workspaceKey(m, "esc")
	m = accountComplete(t, m, cmd)
}

func TestWorkspaceUncertainInitAllowsExplicitReadOnlyView(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var mutations atomic.Int32
	ws := []domain.Workspace{{ID: "one", Root: "/one", Setup: domain.WorkspaceSetup{State: "running", Attempt: &domain.InitAttempt{SessionID: "actual", AccountID: "a", Process: "running", State: "running"}}}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			mutations.Add(1)
			http.Error(w, `{"error":"init_conflict"}`, 409)
			return
		}
		if r.URL.Path == "/v0/sessions" {
			_ = json.NewEncoder(w).Encode([]domain.Session{{ID: "actual", WorkspaceID: "one", AccountID: "a", Adapter: "codex", Work: "init", Process: "running"}})
			return
		}
		workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("a", "codex")}, ws)
	})
	m := accountModel(t, c)
	m.ws = ws
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	m, cmd := workspaceKey(m, "ctrl+enter")
	m = accountComplete(t, m, cmd)
	m, _ = workspaceKey(m, "esc")
	m, cmd = workspaceKey(m, "f")
	m = accountComplete(t, m, cmd)
	m, _ = workspaceKey(m, "]")
	m, cmd = workspaceKey(m, "v")
	if cmd == nil {
		t.Fatal("uncertain mutation blocked explicit read-only recovery")
	}
	next, attach := m.Update(cmd())
	m = next.(model)
	if attach == nil || m.sessID != "actual" || m.workspace.uncertain == nil || mutations.Load() != 1 {
		t.Fatal("view lost uncertainty, used wrong session or mutated")
	}
}
