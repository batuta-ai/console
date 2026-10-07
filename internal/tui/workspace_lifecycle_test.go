package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/domain"
)

func TestWorkspaceNavigationAndExitJoin(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, action := range []string{"back", "quit", "suspend"} {
		for _, dispatched := range []bool{false, true} {
			t.Run(action+map[bool]string{true: "_after", false: "_before"}[dispatched], func(t *testing.T) {
				entered := make(chan struct{})
				var calls atomic.Int32
				c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" {
						calls.Add(1)
						close(entered)
						<-r.Context().Done()
						return
					}
					accountCatalog(w, r, nil)
				})
				m := workspaceCreateDraft(t, accountModel(t, c), "draft", "/root")
				m, create := workspaceKey(m, "enter")
				if create == nil {
					t.Fatal("missing create")
				}
				done := make(chan tea.Msg, 1)
				if dispatched {
					go func() { done <- create() }()
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("request not dispatched")
					}
				}
				var cmd tea.Cmd
				if action == "back" {
					m, cmd = workspaceKey(m, "esc")
				} else {
					next, exit := m.Update(terminalExitRequest{suspend: action == "suspend"})
					m, cmd = next.(model), exit
				}
				if cmd == nil {
					t.Fatal("navigation did not join workspace request")
				}
				joined := make(chan tea.Msg, 1)
				go func() { joined <- cmd() }()
				var msg tea.Msg
				select {
				case msg = <-joined:
				case <-time.After(2 * time.Second):
					t.Fatal("workspace join blocked")
				}
				next, _ := m.Update(msg)
				m = next.(model)
				if m.workspace.pending != nil {
					t.Fatal("pending request not joined")
				}
				if dispatched {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Fatal("command still running")
					}
					if m.workspace.uncertain == nil {
						t.Fatal("lost possible effect")
					}
				} else if m.workspace.uncertain != nil || calls.Load() != 0 {
					t.Fatal("pre-dispatch cancellation mutated or remained uncertain")
				}
				if action == "suspend" {
					next, _ = m.Update(terminalResumed{})
					m = next.(model)
					parent, finish, ok := m.workspaceIO.begin()
					if !ok || parent.Err() != nil {
						t.Fatal("workspace lifetime not restored")
					}
					finish()
				}
				m.workspaceIO.close()
			})
		}
	}
}

func TestWorkspaceRefreshSelectionDraftAndLastGood(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var fail atomic.Bool
	var watched string
	workspaces := []domain.Workspace{{ID: "one", Root: "/one"}, {ID: "two", Root: "/two"}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/workspaces" && fail.Load() {
			http.Error(w, `{"error":"catalog"}`, 500)
			return
		}
		if r.URL.Path == "/v0/watch" {
			watched = r.URL.Query().Get("workspace")
			_ = json.NewEncoder(w).Encode(domain.WatchView{})
			return
		}
		workspaceSetupCatalog(w, r, nil, workspaces)
	})
	m := accountModel(t, c)
	m.ws = workspaces
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "down")
	m, _ = workspaceKey(m, "enter")
	next, _ := m.Update(m.refresh()())
	m = next.(model)
	if watched != "two" {
		t.Fatalf("watched %q instead of selected ID", watched)
	}
	m, _ = workspaceKey(m, "e")
	m, _ = workspaceKey(m, "my stack")
	fail.Store(true)
	next, _ = m.Update(m.refresh()())
	m = next.(model)
	if len(m.ws) != 2 || m.workspace.selected.ID != "two" {
		t.Fatal("failed read lost catalog or selection")
	}
	accountText(t, m, "my stack")
	accountText(t, m, "read failed")
	stale := dataMsg{ws: []domain.Workspace{{ID: "foreign", Root: "/foreign"}}}
	next, _ = m.Update(stale)
	m = next.(model)
	if len(m.ws) != 2 || m.workspace.selected.ID != "two" {
		t.Fatal("unversioned/stale refresh replaced selection/catalog")
	}
}

func TestWorkspaceUncertaintyFocusAndManualInspection(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var calls atomic.Int32
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls.Add(1)
			http.Error(w, `{"error":"workspace","message":"lost result"}`, 409)
			return
		}
		workspaceSetupCatalog(w, r, nil, []domain.Workspace{{ID: "do-not-guess", Name: "draft", Root: "/root"}})
	})
	m := workspaceCreateDraft(t, accountModel(t, c), "draft", "/root")
	m, cmd := workspaceKey(m, "enter")
	m = accountComplete(t, m, cmd)
	accountText(t, m, "Possible workspace effect")
	m, _ = workspaceKey(m, "esc")
	m, cmd = workspaceKey(m, "f")
	if cmd == nil {
		t.Fatal("workspace list inspection unavailable")
	}
	m = accountComplete(t, m, cmd)
	if m.workspace.selected.ID != "" {
		t.Fatal("uncertain create guessed workspace by name/root")
	}
	m, _ = workspaceKey(m, "enter")
	if m.workspace.selected.ID != "do-not-guess" {
		t.Fatal("explicit workspace selection unavailable")
	}
	m, _ = workspaceKey(m, "d")
	if m.workspace.uncertain != nil {
		t.Fatal("explicit inspected acknowledgment unavailable")
	}
	if len(m.workspace.receipts) == 0 {
		t.Fatal("acknowledgment erased possible-effect identity")
	}
	m, _ = workspaceKey(m, "e")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.account.uncertain = &accountRequest{kind: "create", label: "unknown-account", ctx: ctx, cancel: cancel}
	m, _ = workspaceKey(m, "f")
	m, _ = workspaceKey(m, "d")
	accountText(t, m, "Stack: fd")
	if m.account.uncertain == nil {
		t.Fatal("text consumed account recovery")
	}
	if calls.Load() != 1 {
		t.Fatal("uncertain create replayed")
	}
}

func TestWorkspaceStalePickerAndRead(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"path":"/late"}`))
			return
		}
		workspaceSetupCatalog(w, r, nil, []domain.Workspace{{ID: "old", Root: "/old", Setup: domain.WorkspaceSetup{Ready: true, State: "ready"}}})
	})
	m := workspaceCreateDraft(t, accountModel(t, c), "old", "/draft")
	m, cmd := workspaceKey(m, "ctrl+p")
	result := cmd()
	m, join := workspaceKey(m, "esc")
	m = accountComplete(t, m, join)
	m, _ = workspaceKey(m, "n")
	m, _ = workspaceKey(m, "fresh")
	next, _ := m.Update(result)
	m = next.(model)
	if strings.Contains(m.View().Content, "/late") {
		t.Fatal("late picker contaminated fresh draft")
	}
	if m.workspace.name != "fresh" {
		t.Fatal("fresh draft lost")
	}
}

func TestWorkspaceReadBeforeMutationCannotRestoreReadiness(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	ws := []domain.Workspace{{ID: "one", Root: "/one", Setup: domain.WorkspaceSetup{State: "partial"}}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("a", "codex")}, ws)
	})
	m := accountModel(t, c)
	m.ws = ws
	m, _ = workspaceKey(m, "w")
	m, _ = workspaceKey(m, "enter")
	old := m.refresh()()
	m, inspect := workspaceKey(m, "f")
	m = accountComplete(t, m, inspect)
	m.workspace.selected.Setup = domain.WorkspaceSetup{State: "interrupted", Code: "init_interrupted"}
	next, _ := m.Update(old)
	m = next.(model)
	if m.workspace.selected.Setup.State != "interrupted" {
		t.Fatal("old read overwrote newer workspace fact")
	}
}

func TestWorkspaceCancelledInitKeepsDraftAndNoResumeReplay(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	entered := make(chan struct{})
	var calls atomic.Int32
	ws := []domain.Workspace{{ID: "created", Root: "/canonical", Setup: domain.WorkspaceSetup{State: "partial"}}}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls.Add(1)
			close(entered)
			<-r.Context().Done()
			return
		}
		workspaceSetupCatalog(w, r, []domain.Account{workspaceReadyAccount("a", "codex")}, ws)
	})
	m := accountModel(t, c)
	m.ws = ws
	m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "retain this"))
	m, init := workspaceKey(m, "ctrl+enter")
	result := make(chan tea.Msg, 1)
	go func() { result <- init() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no init dispatch")
	}
	next, exit := m.Update(terminalExitRequest{suspend: true})
	m = next.(model)
	next, _ = m.Update(exit())
	m = next.(model)
	next, _ = m.Update(terminalResumed{})
	m = next.(model)
	select {
	case late := <-result:
		next, _ = m.Update(late)
		m = next.(model)
	case <-time.After(time.Second):
		t.Fatal("init did not join")
	}
	if m.workspace.uncertain == nil || m.workspace.selected.ID != "created" || m.workspace.selected.Root != "/canonical" || m.workspace.draft.config() != workspaceConfig() || m.workspace.draft.fields[8] != "retain this" || m.workspace.draft.account.ID != "a" {
		t.Fatal("partial init identity or draft lost")
	}
	if calls.Load() != 1 || m.sessID != "" || m.link != nil {
		t.Fatal("resume replayed or attached")
	}
	m.workspaceIO.close()
}

func TestWorkspaceAcknowledgedUnknownCreateCannotRepeat(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var calls atomic.Int32
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls.Add(1)
			http.Error(w, `{"error":"lost"}`, 500)
			return
		}
		workspaceSetupCatalog(w, r, nil, nil)
	})
	m := workspaceCreateDraft(t, accountModel(t, c), "uncertain", "/unknown")
	m, cmd := workspaceKey(m, "enter")
	m = accountComplete(t, m, cmd)
	m, _ = workspaceKey(m, "esc")
	m, cmd = workspaceKey(m, "f")
	m = accountComplete(t, m, cmd)
	m, _ = workspaceKey(m, "d")
	m = workspaceCreateDraft(t, m, "fresh-name", "/unknown")
	m, cmd = workspaceKey(m, "enter")
	if cmd != nil {
		m = accountComplete(t, m, cmd)
	}
	if calls.Load() != 1 {
		t.Fatal("acknowledgment allowed another create with unresolved effect")
	}
}

func TestWorkspaceListNavigationJoinsBlockedRead(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, key := range []string{"n", "enter", "h", "c"} {
		t.Run(key, func(t *testing.T) {
			entered := make(chan struct{})
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/workspaces" {
					close(entered)
					<-r.Context().Done()
					return
				}
				accountCatalog(w, r, nil)
			})
			m := accountModel(t, c)
			m.ws = []domain.Workspace{{ID: "one", Root: "/one"}}
			m, _ = workspaceKey(m, "w")
			m, read := workspaceKey(m, "f")
			result := make(chan tea.Msg, 1)
			go func() { result <- read() }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("read not dispatched")
			}
			m, join := workspaceKey(m, key)
			if join == nil {
				t.Fatal("navigation did not join owned workspace read")
			}
			finished := make(chan tea.Msg, 1)
			go func() { finished <- join() }()
			select {
			case msg := <-finished:
				next, _ := m.Update(msg)
				m = next.(model)
			case <-time.After(time.Second):
				t.Fatal("read join blocked")
			}
			select {
			case <-result:
			case <-time.After(time.Second):
				t.Fatal("read still running")
			}
			if m.workspace.read != nil {
				t.Fatal("read pointer still pending")
			}
		})
	}
}
