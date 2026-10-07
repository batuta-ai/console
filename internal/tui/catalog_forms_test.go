package tui

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func catalogKey(m model, key string) (model, tea.Cmd) {
	k := tea.KeyPressMsg{Text: key}
	keys := map[string]rune{" ": ' ', "left": tea.KeyLeft, "right": tea.KeyRight, "backspace": tea.KeyBackspace, "ctrl+up": tea.KeyUp, "ctrl+down": tea.KeyDown}
	if code, ok := keys[key]; ok {
		k = tea.KeyPressMsg{Code: code}
		if strings.HasPrefix(key, "ctrl+") {
			k.Mod = tea.ModCtrl
		}
		next, cmd := m.Update(k)
		return next.(model), cmd
	}
	return workspaceKey(m, key)
}
func catalogPaste(m model, text string) model {
	next, _ := m.Update(tea.PasteMsg{Content: text})
	return next.(model)
}
func catalogTabs(m model, n int) model {
	for range n {
		m, _ = catalogKey(m, "tab")
	}
	return m
}
func catalogDraft(t *testing.T, m model, provider string) model {
	t.Helper()
	m, _ = catalogKey(m, "s")
	var cmd tea.Cmd
	m, cmd = catalogKey(m, "n")
	if cmd != nil || m.screen != scrEditor || m.editor.ID != "" || len(m.editor.Members) != 0 {
		t.Fatal("new squad must open locally, with no POST or invented identity")
	}
	m = catalogPaste(m, "qns vfd Squad")
	m = catalogTabs(m, 1)
	m = catalogPaste(m, "Purpose\nwith detail")
	m, _ = catalogKey(m, "ctrl+n")
	m = catalogPaste(m, "Lead")
	m = catalogTabs(m, 1)
	m, _ = catalogKey(m, "right")
	if provider == "claude" {
		m, _ = catalogKey(m, "right")
	}
	m = catalogTabs(m, 1)
	m, _ = catalogKey(m, "right")
	m = catalogTabs(m, 1)
	m = catalogPaste(m, "chosen-model")
	m = catalogTabs(m, 1)
	m = catalogPaste(m, "high")
	m = catalogTabs(m, 1)
	m = catalogPaste(m, "skill-one,skill-two")
	m = catalogTabs(m, 1)
	m, _ = catalogKey(m, " ")
	return m
}
func TestCatalogSquadLocalFormsExactSaveValidate(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var saves, validates []domain.Squad
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					accountCatalog(w, r, nil)
					return
				}
				var q domain.Squad
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
				}
				switch r.URL.Path {
				case "/v0/squads/draft/validate", "/v0/squads/actual-squad/validate":
					validates = append(validates, q)
					q.ID = "draft"
					_ = json.NewEncoder(w).Encode(client.SquadValidation{Squad: q, Validation: domain.Validation{Title: "Authentication pending", Text: "No model invoked", Ready: false}})
				case "/v0/squads":
					saves = append(saves, cloneSquad(q))
					q.ID = "actual-squad"
					q.Version = len(saves)
					q.Origin = "custom"
					q.Draft = true
					for i := range q.Members {
						q.Members[i].ID = "real-member"
					}
					_ = json.NewEncoder(w).Encode(q)
				default:
					t.Errorf("unexpected write: %s", r.URL.Path)
					http.Error(w, "bad", 500)
				}
			})
			m := New(c).(model)
			m.accounts = []domain.Account{{ID: "wrong-first", Adapter: "other"}, {ID: "exact-account", Adapter: provider}}
			m = catalogDraft(t, m, provider)
			want := domain.Member{Role: "Lead", CLI: provider, AccountID: "exact-account", Model: "chosen-model", Effort: "high", Skills: []string{"skill-one", "skill-two"}, Maestro: true}
			if !reflect.DeepEqual(m.editor.Members, []domain.Member{want}) {
				t.Fatalf("explicit fields: %+v", m.editor)
			}
			accountText(t, m, "chosen-model")
			accountText(t, m, "exact-account")
			m, cmd := catalogKey(m, "ctrl+v")
			if cmd == nil {
				t.Fatal("validation missing")
			}
			m = accountComplete(t, m, cmd)
			if m.editor.ID != "" {
				t.Fatal("validation placeholder persisted")
			}
			accountText(t, m, "Authentication pending")
			for range 2 {
				m, cmd = catalogKey(m, "ctrl+s")
				if cmd == nil {
					t.Fatal("save missing")
				}
				var duplicate tea.Cmd
				m, duplicate = catalogKey(m, "ctrl+s")
				if duplicate != nil {
					t.Fatal("duplicate save")
				}
				m = accountComplete(t, m, cmd)
			}
			if len(saves) != 2 || len(validates) != 1 || saves[0].ID != "" || saves[1].ID != "actual-squad" || saves[1].Members[0].ID != "real-member" || m.editor.Version != 2 || !m.editor.Draft {
				t.Fatalf("identity chain: %+v %+v", saves, m.editor)
			}
			if !reflect.DeepEqual(saves[0].Members[0], want) {
				t.Fatal("typed body differs")
			}
			m, _ = catalogKey(m, "esc")
			m, _ = catalogKey(m, "n")
			if m.editor.ID != "" || m.editor.Name != "" || m.editor.Version != 0 || m.editor.Origin != "" || len(m.editor.Members) != 0 {
				t.Fatal("second form reused saved fields")
			}
		})
	}
}
func TestCatalogExistingSquadsDeepCopyAndMemberSelection(t *testing.T) {
	m := New(nil).(model)
	q := domain.Squad{ID: "sq1", Version: 7, Origin: "batuta", Name: "One", Members: []domain.Member{{ID: "member1", Role: "Lead", CLI: "codex", AccountID: "account1", Skills: []string{"keep"}}, {ID: "member2", Role: "Second", CLI: "claude", AccountID: "account2"}}}
	m.squads = []domain.Squad{q, {ID: "sq2", Name: "Two", Version: 3, Origin: "custom"}}
	m.missions = []domain.Mission{{ID: "mission1", SquadSnapshot: q}}
	m, _ = catalogKey(m, "s")
	m, _ = catalogKey(m, "enter")
	m = catalogTabs(m, 2)
	m = catalogPaste(m, " edited")
	m, _ = catalogKey(m, "ctrl+down")
	m = catalogPaste(m, " selected")
	if m.editor.Members[0].Role != "Lead edited" || m.editor.Members[1].Role != "Second selected" {
		t.Fatal("member selection did not route edits")
	}
	m = catalogTabs(m, 5)
	m = catalogPaste(m, "new-skill")
	if q.Members[0].Role != "Lead" || m.missions[0].SquadSnapshot.Members[1].Role != "Second" || !reflect.DeepEqual(q.Members[0].Skills, []string{"keep"}) {
		t.Fatal("catalog or snapshot alias corrupted")
	}
	if m.editor.ID != "sq1" || m.editor.Version != 7 || m.editor.Origin != "batuta" || m.editor.Members[1].ID != "member2" {
		t.Fatal("identity lost")
	}
	m, _ = catalogKey(m, "ctrl+x")
	if len(m.editor.Members) != 1 || len(q.Members) != 2 {
		t.Fatal("remove aliases catalog")
	}
	m, _ = catalogKey(m, "esc")
	m, _ = catalogKey(m, "down")
	m, _ = catalogKey(m, "enter")
	if m.editor.ID != "sq2" || m.editor.Name != "Two" || m.editor.Version != 3 || len(m.editor.Members) != 0 {
		t.Fatal("second selection contaminated")
	}
}
func TestCatalogMissionReviewCreatesAndSelection(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var bodies []client.CreateMissionRequest
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			accountCatalog(w, r, nil)
			return
		}
		if r.URL.Path != "/v0/missions" {
			t.Errorf("unexpected write %s", r.URL.Path)
		}
		var in client.CreateMissionRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		bodies = append(bodies, in)
		q := domain.Squad{ID: in.SquadID, Name: "Canonical", Version: 9, Members: []domain.Member{{ID: "canonical-member", AccountID: "canonical-account"}}}
		_ = json.NewEncoder(w).Encode(domain.Mission{ID: "mission-" + in.Title, WorkspaceID: in.WorkspaceID, Title: in.Title, Brief: in.Brief, SquadID: in.SquadID, SquadSnapshot: q, Status: "open"})
	})
	m := New(c).(model)
	m.ws = []domain.Workspace{{ID: "wrong-workspace"}, {ID: "chosen-workspace"}}
	m.squads = []domain.Squad{{ID: "wrong-squad"}, {ID: "chosen-squad", Version: 2}}
	m.sessID = "unrelated-terminal"
	m.generation = 23
	var cmd tea.Cmd
	for _, title := range []string{"first objective", "second objective"} {
		m, _ = catalogKey(m, "m")
		m, _ = catalogKey(m, "n")
		accountText(t, m, "New mission")
		m, cmd = catalogKey(m, "ctrl+enter")
		if cmd != nil || len(bodies) != len(m.missions) {
			t.Fatal("empty objective dispatched")
		}
		m = catalogPaste(m, title)
		m = catalogTabs(m, 1)
		m = catalogPaste(m, "qns vfd\nactual brief")
		m = catalogTabs(m, 1)
		if m.workspace.selected.ID == "" {
			m, _ = catalogKey(m, "right")
			m, _ = catalogKey(m, "right")
		}
		m = catalogTabs(m, 1)
		m, _ = catalogKey(m, "right")
		m, _ = catalogKey(m, "right")
		m, cmd = catalogKey(m, "ctrl+enter")
		if cmd != nil {
			t.Fatal("review dispatched")
		}
		accountText(t, m, "Confirm catalog creation")
		m, cmd = catalogKey(m, "ctrl+enter")
		if cmd == nil {
			t.Fatal("confirmation missing")
		}
		var duplicate tea.Cmd
		m, duplicate = catalogKey(m, "ctrl+enter")
		if duplicate != nil {
			t.Fatal("duplicate mission")
		}
		m = accountComplete(t, m, cmd)
		if m.missionID != "mission-"+title || m.sessID != "unrelated-terminal" || m.generation != 23 || m.termMode {
			t.Fatal("mission/terminal identity corrupted")
		}
		accountText(t, m, "canonical-member")
		accountText(t, m, "v9")
		accountText(t, m, "chosen-workspace")
	}
	if len(bodies) != 2 {
		t.Fatal(bodies)
	}
	for i, title := range []string{"first objective", "second objective"} {
		if bodies[i] != (client.CreateMissionRequest{WorkspaceID: "chosen-workspace", SquadID: "chosen-squad", Title: title, Brief: "qns vfd\nactual brief"}) {
			t.Fatalf("body %+v", bodies[i])
		}
	}
	m, _ = catalogKey(m, "m")
	m, _ = catalogKey(m, "enter")
	if m.missionID != "mission-first objective" {
		t.Fatal("first mission not selected by ID")
	}
	m, _ = catalogKey(m, "m")
	m, _ = catalogKey(m, "down")
	m, _ = catalogKey(m, "enter")
	if m.missionID != "mission-second objective" {
		t.Fatal("second mission not selected by ID")
	}
}
