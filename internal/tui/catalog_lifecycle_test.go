package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func catalogSimple(m model) model {
	m.screen = scrSquads
	m, _ = catalogKey(m, "n")
	return catalogPaste(m, "Kept draft")
}
func catalogFinish(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = catalogFinish(t, m, c)
		}
		return m
	}
	next, more := m.Update(msg)
	return catalogFinish(t, next.(model), more)
}
func TestCatalogLifecycleCancelBeforeAndAfterDispatch(t *testing.T) {
	for _, kind := range []string{"save", "validate", "create", "read"} {
		for _, after := range []bool{false, true} {
			for _, exit := range []string{"navigate", "quit", "suspend"} {
				t.Run(kind+"/"+map[bool]string{true: "after", false: "before"}[after]+"/"+exit, func(t *testing.T) {
					var calls atomic.Int32
					entered := make(chan struct{})
					joined := make(chan struct{})
					c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.Copy(io.Discard, r.Body)
						calls.Add(1)
						close(entered)
						defer close(joined)
						<-r.Context().Done()
					})
					m := catalogSimple(New(c).(model))
					m.workspace.refresh = nil
					m.account.read = nil
					m.ws = []domain.Workspace{{ID: "chosen-w"}}
					m.squads = []domain.Squad{{ID: "chosen-s", Version: 1}}
					var cmd tea.Cmd
					if kind == "create" {
						m = m.startMission()
						m.catalog.mission = missionForm{title: "real objective", workspaceID: "chosen-w", squad: m.squads[0], reviewing: true}
					}
					if kind == "read" {
						cmd = m.readCatalog()
					} else {
						m, cmd = m.catalogMutation(kind)
					}
					if cmd == nil {
						t.Fatal("missing request")
					}
					r := m.catalog.pending
					if kind == "read" {
						r = m.catalog.read
					}
					deadline, ok := r.ctx.Deadline()
					if !ok || time.Until(deadline) > catalogTimeout {
						t.Fatal("unbounded request")
					}
					result := make(chan tea.Msg, 1)
					if after {
						go func() { result <- cmd() }()
						select {
						case <-entered:
						case <-time.After(2 * time.Second):
							t.Fatal("not dispatched")
						}
					}
					var cancel tea.Cmd
					switch exit {
					case "navigate":
						m, cancel = catalogKey(m, "esc")
					case "quit":
						m, cancel = m.exitAccountWork(false)
					case "suspend":
						m, cancel = m.exitAccountWork(true)
					}
					if exit == "navigate" {
						m = catalogFinish(t, m, cancel)
					} else {
						out := cancel().(accountJoined)
						for _, res := range out.catalog {
							m, _ = m.applyCatalogResult(res)
						}
						if _, ok := out.exit.(terminalExitReady); !ok {
							t.Fatal("exit before join")
						}
					}
					if after {
						select {
						case <-joined:
						case <-time.After(2 * time.Second):
							t.Fatal("HTTP not joined")
						}
						m = catalogFinish(t, m, func() tea.Msg { return <-result })
					}
					if !after {
						m = catalogFinish(t, m, cmd)
						if calls.Load() != 0 {
							t.Fatal("canceled before dispatch ran HTTP")
						}
					} else if calls.Load() != 1 {
						t.Fatal("duplicate HTTP")
					}
					if m.catalog.pending != nil || m.catalog.read != nil {
						t.Fatal("pending request not joined")
					}
					if (kind == "save" || kind == "create") && after && m.catalog.uncertain == nil {
						t.Fatal("lost effect not retained")
					}
					if kind == "validate" && m.catalog.uncertain != nil {
						t.Fatal("validation mislabeled as save")
					}
					if m.editor.Name != "Kept draft" {
						t.Fatal("draft lost")
					}
					if exit == "suspend" {
						next, replay := m.Update(terminalResumed{})
						m = next.(model)
						if replay != nil || m.catalogIO == r.lifetime {
							t.Fatal("resume replayed or reused closed lifetime")
						}
					}
				})
			}
		}
	}
}
func TestCatalogStaleValidationAndReads(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewEncoder(w).Encode(client.SquadValidation{Validation: domain.Validation{Title: "old validation", Ready: true}})
			return
		}
		if r.URL.Path == "/v0/squads" {
			_ = json.NewEncoder(w).Encode([]domain.Squad{{ID: "late"}})
			return
		}
		_ = json.NewEncoder(w).Encode([]domain.Mission{{ID: "late-mission", WorkspaceID: "foreign"}})
	})
	m := catalogSimple(New(c).(model))
	m.workspace.refresh = nil
	m, cmd := catalogKey(m, "ctrl+v")
	out := cmd()
	m = catalogPaste(m, " changed")
	m = catalogFinish(t, m, func() tea.Msg { return out })
	if m.catalog.validation != nil || m.editor.Name != "Kept draft changed" {
		t.Fatal("old validation restored")
	}
	m.squads = []domain.Squad{{ID: "confirmed"}}
	m.missions = []domain.Mission{{ID: "first", WorkspaceID: "w1", SquadSnapshot: domain.Squad{ID: "s1", Version: 2}}, {ID: "second", WorkspaceID: "w2", SquadSnapshot: domain.Squad{ID: "s2", Version: 3}}}
	read := m.readCatalog()
	old := read()
	m.screen = scrMissions
	m.cursor = 1
	m, _ = catalogKey(m, "enter")
	m = catalogFinish(t, m, func() tea.Msg { return old })
	if m.missionID != "second" || m.workspace.selected.ID != "w2" || m.catalog.selected.SquadSnapshot.ID != "s2" || m.squads[0].ID != "confirmed" {
		t.Fatal("late read changed selected authority")
	}
	accountText(t, m, "second")
	accountText(t, m, "w2")
	accountText(t, m, "v3")
}
func TestCatalogReadFailurePreservesLastConfirmed(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"error":"failed"}`, 500) })
	m := catalogSimple(New(c).(model))
	m.squads = []domain.Squad{{ID: "kept"}}
	m.missions = []domain.Mission{{ID: "kept-mission"}}
	m = catalogFinish(t, m, m.readCatalog())
	if m.squads[0].ID != "kept" || m.missions[0].ID != "kept-mission" || m.editor.Name != "Kept draft" {
		t.Fatal("failed read erased data")
	}
}
func TestCatalogUncertainInspectionNeverReplays(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, kind := range []string{"save", "create", "validate"} {
		for _, failure := range []string{"400", "409", "500", "malformed", "foreign", "lost"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				var writes atomic.Int32
				c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" {
						writes.Add(1)
						switch failure {
						case "400":
							http.Error(w, `{"error":"invalid"}`, 400)
						case "409":
							http.Error(w, `{"error":"conflict"}`, 409)
						case "500":
							http.Error(w, `{"error":"lost"}`, 500)
						case "malformed":
							_, _ = w.Write([]byte(`{`))
						case "foreign":
							_ = json.NewEncoder(w).Encode(domain.Squad{ID: "foreign", Version: 2})
						case "lost":
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
							} else {
								_ = conn.Close()
							}
						}
						return
					}
					if r.URL.Path == "/v0/squads" {
						_ = json.NewEncoder(w).Encode([]domain.Squad{{ID: "inspect-actual", Name: "Kept draft", Version: 4}})
					} else {
						_ = json.NewEncoder(w).Encode([]domain.Mission{{ID: "inspect-mission", Title: "same title", WorkspaceID: "w", SquadID: "confirmed-save", SquadSnapshot: domain.Squad{ID: "confirmed-save", Version: 5}}})
					}
				})
				m := catalogSimple(New(c).(model))
				m.editor.ID = "confirmed-save"
				m.editor.Version = 3
				if kind == "create" {
					m.ws = []domain.Workspace{{ID: "w"}}
					m.squads = []domain.Squad{{ID: "confirmed-save", Version: 3}}
					m = m.startMission()
					m.catalog.mission = missionForm{title: "same title", brief: "keep me", workspaceID: "w", squad: m.squads[0], reviewing: true}
				}
				m, cmd := m.catalogMutation(kind)
				m = catalogFinish(t, m, cmd)
				if m.editor.ID != "confirmed-save" || m.editor.Version != 3 {
					t.Fatal("lost partial confirmed save")
				}
				if kind == "validate" {
					if m.catalog.uncertain != nil || m.catalog.problem != "catalog_validation_failed" {
						t.Fatal("failed validation is not a catalog write")
					}
					return
				}
				if m.catalog.uncertain == nil || len(m.catalog.receipts) != 1 {
					t.Fatal("uncertain context lost")
				}
				m, cmd = catalogKey(m, "ctrl+f")
				m = catalogFinish(t, m, cmd)
				if m.editor.ID != "confirmed-save" || m.missionID != "" {
					t.Fatal("inspection inferred success by name")
				}
				m, _ = catalogKey(m, "ctrl+d")
				m, cmd = m.catalogMutation(kind)
				if cmd != nil || writes.Load() != 1 {
					t.Fatal("acknowledgment allowed unknown replay")
				}
				if kind == "create" && (m.catalog.mission.title != "same title" || m.catalog.mission.brief != "keep me") {
					t.Fatal("objective lost")
				}
				m.screen = scrSquads
				accountText(t, m, "confirmed-save")
				accountText(t, m, "Kept draft")
				m.cursor = 0
				m, _ = catalogKey(m, "enter")
				if m.editor.ID != "inspect-actual" {
					t.Fatal("explicit inspection selection failed")
				}
			})
		}
	}
}
func TestCatalogSafeFailureRequiresNewAction(t *testing.T) {
	for _, effect := range []client.OperationEffect{client.EffectRejected, client.EffectNotDispatched} {
		var calls int
		c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, `{"error":"unauthorized"}`, 401) })
		m := catalogSimple(New(c).(model))
		m, cmd := m.catalogMutation("save")
		if effect == client.EffectNotDispatched {
			m.catalog.pending.cancel()
		}
		m = catalogFinish(t, m, cmd)
		if m.catalog.uncertain != nil || len(m.catalog.receipts) != 0 {
			t.Fatal("safe rejection quarantined")
		}
		if (effect == client.EffectRejected && calls != 1) || (effect == client.EffectNotDispatched && calls != 0) {
			t.Fatal("unexpected dispatch count")
		}
		_, retry := catalogKey(m, "ctrl+s")
		if retry == nil {
			t.Fatal("explicit safe retry unavailable")
		}
	}
}
func TestCatalogFocusMutualExclusionAndValidationInvalidation(t *testing.T) {
	m := catalogSimple(New(client.New("http://unused", "")).(model))
	m.account.uncertain = &accountRequest{}
	m.workspace.uncertain = &workspaceRequest{}
	for _, key := range []string{"q", "n", "s", "v", "f", "d"} {
		var cmd tea.Cmd
		m, cmd = catalogKey(m, key)
		if cmd != nil || m.screen != scrEditor || m.exiting {
			t.Fatal("text became shortcut")
		}
	}
	if !strings.HasSuffix(m.editor.Name, "qnsvfd") {
		t.Fatal("input lost")
	}
	m.account.uncertain = nil
	m.workspace.uncertain = nil
	m, cmd := catalogKey(m, "ctrl+s")
	if cmd == nil {
		t.Fatal("save missing")
	}
	if _, other := m.accountMutation("create"); other != nil {
		t.Fatal("concurrent account write")
	}
	if _, other := m.workspaceMutation("create"); other != nil {
		t.Fatal("concurrent workspace write")
	}
	m.catalog.pending.cancel()
	for _, which := range []string{"account", "workspace"} {
		next := catalogSimple(New(client.New("http://unused", "")).(model))
		if which == "account" {
			next.account.pending = &accountRequest{}
		} else {
			next.workspace.pending = &workspaceRequest{}
		}
		if _, other := next.catalogMutation("save"); other != nil {
			t.Fatal("concurrent catalog write")
		}
	}
	// Changes to the account catalog invalidate a previously ready validation.
	m = catalogSimple(New(nil).(model))
	m.catalog.validation = &domain.Validation{Ready: true}
	m.accounts = []domain.Account{{ID: "a", Adapter: "codex"}}
	r := m.newAccountRequest("read")
	m.account.read = r
	m, _ = m.applyAccountResult(accountResult{request: r, accounts: []domain.Account{{ID: "a", Adapter: "claude"}}})
	if m.catalog.validation != nil {
		t.Fatal("changed account retained stale validation")
	}
}
func TestCatalogSnapshotDoesNotAliasAndFormsReset(t *testing.T) {
	m := New(nil).(model)
	q := domain.Squad{ID: "q", Version: 5, Members: []domain.Member{{ID: "mb", Skills: []string{"original"}}}}
	m.missions = []domain.Mission{{ID: "one", WorkspaceID: "w1", SquadSnapshot: q}, {ID: "two", WorkspaceID: "w2", SquadSnapshot: q}}
	m.screen = scrMissions
	m, _ = catalogKey(m, "enter")
	m.missions[0].SquadSnapshot.Members[0].Skills[0] = "changed"
	if !reflect.DeepEqual(m.catalog.selected.SquadSnapshot.Members[0].Skills, []string{"original"}) {
		t.Fatal("selected immutable snapshot aliases catalog")
	}
}

func TestCatalogStartupReadOwnedByModel(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/squads":
			_ = json.NewEncoder(w).Encode([]domain.Squad{{ID: "startup-squad", Version: 1}})
		case "/v0/missions":
			_ = json.NewEncoder(w).Encode([]domain.Mission{{ID: "startup-mission", WorkspaceID: "w"}})
		default:
			accountCatalog(w, r, nil)
		}
	})
	m := New(c).(model)
	batch := m.Init()().(tea.BatchMsg)
	// Tick schedules ongoing work; process just startup commands here.
	for _, cmd := range batch[:len(batch)-1] {
		m = catalogFinish(t, m, cmd)
	}
	if len(m.squads) != 1 || m.squads[0].ID != "startup-squad" || len(m.missions) != 1 || m.missions[0].ID != "startup-mission" {
		t.Fatal("startup result has no model-owned request")
	}
}
