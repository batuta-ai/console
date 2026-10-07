package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func workspaceReadyAccount(id, adapter string) domain.Account {
	a := accountKnown(id, adapter)
	a.Readiness.Ready = true
	a.Readiness.Authenticated = true
	return a
}
func workspaceConfig() domain.SetupConfig {
	return domain.SetupConfig{Stack: "Go TUI", Methodology: "TDD always", Test: "go test ./...", Build: "go build ./...", Install: "go mod download", Execution: "sequential", Worktree: "medium+", Template: "templates/react.md"}
}
func workspaceSetupDraft(t *testing.T, m model, config domain.SetupConfig, brief string) model {
	t.Helper()
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "enter")
	m, _ = workspaceKey(m, "e")
	accountText(t, m, "Stack")
	for _, value := range []string{config.Stack, config.Methodology, config.Test, config.Build, config.Install, config.Execution, config.Worktree, config.Template, brief} {
		next, _ := m.Update(tea.PasteMsg{Content: value})
		m = next.(model)
		m, _ = workspaceKey(m, "tab")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = next.(model)
	return m
}
func workspaceReview(t *testing.T, m model) model {
	t.Helper()
	m, cmd := workspaceKey(m, "ctrl+r")
	if cmd != nil {
		t.Fatal("review dispatched mutation")
	}
	accountText(t, m, "Confirm setup")
	return m
}
func workspaceSetupCatalog(w http.ResponseWriter, r *http.Request, accounts []domain.Account, ws []domain.Workspace) {
	if r.URL.Path == "/v0/workspaces" {
		_ = json.NewEncoder(w).Encode(ws)
		return
	}
	if r.URL.Path == "/v0/watch" {
		_ = json.NewEncoder(w).Encode(domain.WatchView{})
		return
	}
	accountCatalog(w, r, accounts)
}
func TestWorkspaceSetupExactConfirmation(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			var calls atomic.Int32
			config := workspaceConfig()
			brief := "First line\nSecond line"
			ws := []domain.Workspace{{ID: "exact-workspace", Name: "actual", Root: "/canonical", Setup: domain.WorkspaceSetup{State: "partial"}}}
			accounts := []domain.Account{workspaceReadyAccount("exact-account", adapter)}
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					calls.Add(1)
					if r.URL.Path != "/v0/workspaces/exact-workspace/init" {
						t.Errorf("path=%s", r.URL.Path)
					}
					var in client.InitWorkspaceRequest
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Error(err)
					}
					want := client.InitWorkspaceRequest{AccountID: "exact-account", Config: config, Brief: brief, Owner: "tui", Client: "this-tui"}
					if in != want {
						t.Errorf("body=%+v want=%+v", in, want)
					}
					_ = json.NewEncoder(w).Encode(domain.Session{ID: "actual-init", WorkspaceID: "exact-workspace", AccountID: "exact-account", Adapter: adapter, Work: "init", Process: "running", Owner: "web", OwnerClient: "other-client"})
					return
				}
				workspaceSetupCatalog(w, r, accounts, ws)
			})
			m := accountModel(t, c)
			m.ws = ws
			m.clientID = "this-tui"
			m = workspaceSetupDraft(t, m, config, brief)
			m = workspaceReview(t, m)
			if calls.Load() != 0 {
				t.Fatal("mutation before confirmation")
			}
			m, cmd := workspaceKey(m, "ctrl+enter")
			if cmd == nil {
				t.Fatal("confirmed init not dispatched")
			}
			m, duplicate := workspaceKey(m, "ctrl+enter")
			if duplicate != nil {
				t.Fatal("double confirmation")
			}
			next, attach := m.Update(cmd())
			m = next.(model)
			if calls.Load() != 1 || m.sessID != "actual-init" || m.screen != scrRoom || attach == nil || m.termMode {
				t.Fatalf("incorrect init attachment: calls=%d id=%s screen=%s attach=%v", calls.Load(), m.sessID, m.screen, attach != nil)
			}
			accountText(t, m, "web")
			if ws[0].Setup.Ready {
				t.Fatal("spawn proved readiness")
			}
		})
	}
}
func TestWorkspaceSetupValidationAndPreflight(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, failure := range []string{"empty", "long", "newline", "nul", "invalid_utf8", "execution", "worktree", "notes_long", "notes_nul", "unknown", "unauthenticated", "incompatible", "failed_probe", "unavailable", "missing", "ready"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			a := workspaceReadyAccount("account", "codex")
			config := workspaceConfig()
			brief := "notes"
			ws := []domain.Workspace{{ID: "actual", Root: "/actual", Setup: domain.WorkspaceSetup{State: "partial"}}}
			fresh := []domain.Account{a}
			switch failure {
			case "empty":
				config.Stack = ""
			case "long":
				config.Stack = strings.Repeat("x", 8193)
			case "newline":
				config.Test = "x\ny"
			case "nul":
				config.Build = "x\x00y"
			case "invalid_utf8":
				config.Stack = "x\xff"
			case "execution":
				config.Execution = "guess"
			case "worktree":
				config.Worktree = "guess"
			case "notes_long":
				brief = strings.Repeat("n", 65537)
			case "notes_nul":
				brief = "n\x00"
			case "unknown":
				fresh[0].Readiness = nil
			case "unauthenticated":
				a.Readiness.Authenticated = false
			case "incompatible":
				a.Readiness.CLICompatible = false
			case "failed_probe":
				a.Readiness.Code = "cli_probe_failed"
			case "unavailable":
				a.Readiness.Available = false
			case "missing":
				fresh = nil
			case "ready":
				ws[0].Setup.Ready = true
				ws[0].Setup.State = "ready"
			}
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					calls.Add(1)
					http.Error(w, `{"error":"unexpected"}`, 500)
					return
				}
				workspaceSetupCatalog(w, r, fresh, ws)
			})
			m := accountModel(t, c)
			m.accounts = []domain.Account{workspaceReadyAccount("account", "codex")}
			m.ws = []domain.Workspace{{ID: "actual", Root: "/actual", Setup: domain.WorkspaceSetup{State: "partial"}}}
			m = workspaceSetupDraft(t, m, config, brief)
			m, _ = workspaceKey(m, "ctrl+r")
			m, cmd := workspaceKey(m, "ctrl+enter")
			if cmd != nil {
				m = accountComplete(t, m, cmd)
			}
			if calls.Load() != 0 {
				t.Fatal("invalid setup mutated")
			}
			accountText(t, m, "/actual")
			if m.sessID != "" || m.screen == scrRoom {
				t.Fatal("preflight attached")
			}
		})
	}
}

func TestWorkspaceReviewPlainEnterConfirms(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var calls atomic.Int32
	ws := []domain.Workspace{{ID: "one", Root: "/one"}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls.Add(1)
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("a", "codex")}, ws)
	})
	m := accountModel(t, c)
	m.ws = ws
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	m, cmd := workspaceKey(m, "enter")
	if cmd == nil {
		t.Fatal("review has no ordinary-terminal confirmation key")
	}
	m = accountComplete(t, m, cmd)
	if calls.Load() != 1 || m.workspace.uncertain != nil {
		t.Fatal("explicit rejected init must be manually retryable")
	}
}

func TestWorkspaceInitDisplaysReportedOwnerClient(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	ws := []domain.Workspace{{ID: "one", Root: "/one"}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewEncoder(w).Encode(domain.Session{ID: "init", WorkspaceID: "one", AccountID: "a", Adapter: "codex", Work: "init", Process: "running", Owner: "web", OwnerClient: "actual-owner-client"})
			return
		}
		workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("a", "codex")}, ws)
	})
	m := accountModel(t, c)
	m.ws = ws
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	m, cmd := workspaceKey(m, "ctrl+enter")
	next, _ := m.Update(cmd())
	m = next.(model)
	accountText(t, m, "actual-owner-client")
	accountText(t, m, "web")
}
