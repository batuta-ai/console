package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestWorkspaceFreshReadinessInvalidatesReview(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	a := workspaceReadyAccount("a", "codex")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { accountCatalog(w, r, []domain.Account{a}) })
	m := accountModel(t, c)
	m.ws = []domain.Workspace{{ID: "one", Root: "/one"}}
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	a = accountKnown("a", "codex")
	read := m.readAccounts()
	m = accountComplete(t, m, read)
	if m.workspace.draft.reviewing {
		t.Fatal("fresh unauthenticated read retained confirmation")
	}
	if m.workspace.draft.account.Readiness.Authenticated {
		t.Fatal("workspace displayed stale authenticated account")
	}
	if m.workspace.draft.config() != workspaceConfig() || m.workspace.focus != 9 {
		t.Fatal("fresh readiness lost config or focus")
	}
	accountText(t, m, "Authentication pending")
}

func TestWorkspaceFreshPreflightFailureShowsActualReadiness(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	ws := []domain.Workspace{{ID: "one", Root: "/one"}}
	a := workspaceReadyAccount("a", "codex")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { workspaceSetupCatalog(w, r, []domain.Account{a}, ws) })
	m := accountModel(t, c)
	m.ws = ws
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "notes"))
	a = accountKnown("a", "codex")
	m, cmd := workspaceKey(m, "ctrl+enter")
	m = accountComplete(t, m, cmd)
	if strings.Contains(m.View().Content, "Account ready") {
		t.Fatal("failed preflight still displayed Account ready")
	}
	if m.workspace.draft.config() != workspaceConfig() || m.workspace.focus != 9 {
		t.Fatal("preflight lost inputs/focus")
	}
}

func TestWorkspaceWatchErrorKeepsLastConfirmedWatch(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/watch" {
			http.Error(w, `{"error":"catalog"}`, 500)
			return
		}
		if r.URL.Path == "/v0/workspaces" {
			_ = json.NewEncoder(w).Encode([]domain.Workspace{{ID: "one", Root: "/one"}})
			return
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	m.ws = []domain.Workspace{{ID: "one", Root: "/one"}}
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "enter")
	m.watch = domain.WatchView{Message: "last good watch"}
	next, _ := m.Update(m.refresh()())
	m = next.(model)
	if m.watch.Message != "last good watch" {
		t.Fatal("watch error erased last good read")
	}
	accountText(t, m, "read failed")
}
