package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func workspaceKey(m model, key string) (model, tea.Cmd) {
	if strings.HasPrefix(key, "ctrl+") {
		code := rune(key[len(key)-1])
		if key == "ctrl+enter" {
			code = tea.KeyEnter
		}
		next, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl})
		return next.(model), cmd
	}
	return accountKey(m, key)
}

func workspaceCreateDraft(t *testing.T, m model, name, root string) model {
	t.Helper()
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "n")
	accountText(t, m, "New workspace")
	m, _ = workspaceKey(m, name)
	m, _ = workspaceKey(m, "tab")
	m, _ = workspaceKey(m, root)
	return m
}

func TestWorkspaceHomeAndCanonicalCreates(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var mu sync.Mutex
	var bodies []client.CreateWorkspaceRequest
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v0/workspaces" {
			var in client.CreateWorkspaceRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			mu.Lock()
			bodies = append(bodies, in)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(domain.Workspace{ID: "actual-" + in.Name, Name: "registered", Root: "/canonical/" + in.Name, Setup: domain.WorkspaceSetup{State: "missing", Code: "profile_missing"}})
			return
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	accountText(t, m, "w workspaces")
	for _, name := range []string{"first", "second"} {
		m = workspaceCreateDraft(t, m, name, "/typed/"+name)
		var cmd tea.Cmd
		m, cmd = workspaceKey(m, "enter")
		if cmd == nil {
			t.Fatal("create not dispatched")
		}
		var duplicate tea.Cmd
		m, duplicate = workspaceKey(m, "enter")
		if duplicate != nil {
			t.Fatal("double create")
		}
		m = accountComplete(t, m, cmd)
		accountText(t, m, "actual-"+name)
		accountText(t, m, "/canonical/"+name)
		m, _ = workspaceKey(m, "esc")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != (client.CreateWorkspaceRequest{Name: "first", Root: "/typed/first"}) || bodies[1] != (client.CreateWorkspaceRequest{Name: "second", Root: "/typed/second"}) {
		t.Fatalf("bodies: %+v", bodies)
	}
}

func TestWorkspacePickerSelectedCancelledUnavailable(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, outcome := range []string{"selected", "cancelled", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			var calls int
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					calls++
					if r.URL.Path != "/v0/workspaces/pick-root" {
						t.Errorf("unexpected mutation %s", r.URL.Path)
					}
					if outcome == "unavailable" {
						http.Error(w, `{"error":"picker_unavailable"}`, 503)
						return
					}
					_ = json.NewEncoder(w).Encode(client.PickRootResult{Path: map[string]string{"selected": "/picked"}[outcome], Canceled: outcome == "cancelled"})
					return
				}
				accountCatalog(w, r, nil)
			})
			m := workspaceCreateDraft(t, accountModel(t, c), "draft", "/manual")
			m, cmd := workspaceKey(m, "ctrl+p")
			if cmd == nil {
				t.Fatal("picker not dispatched")
			}
			m = accountComplete(t, m, cmd)
			want := map[string]string{"selected": "/picked", "cancelled": "/manual", "unavailable": "/manual"}[outcome]
			accountText(t, m, want)
			accountText(t, m, "New workspace")
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestWorkspaceExistingHomeEnterUnchanged(t *testing.T) {
	m := New(nil).(model)
	m.ws = []domain.Workspace{{ID: "one", Name: "One", Root: "/one"}, {ID: "two", Name: "Two", Root: "/two"}}
	m, _ = workspaceKey(m, "enter")
	if m.screen != scrMissions {
		t.Fatal("Home enter changed")
	}
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "down")
	m, _ = workspaceKey(m, "enter")
	accountText(t, m, "two")
	accountText(t, m, "/two")
}
