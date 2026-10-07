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

func TestCatalogLateSaveRetainsReceiptWithoutChangingSelection(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var q domain.Squad
		_ = json.NewDecoder(r.Body).Decode(&q)
		q.ID = "saved-actual"
		q.Version = 1
		_ = json.NewEncoder(w).Encode(q)
	})
	m := catalogSimple(New(c).(model))
	m, cmd := catalogKey(m, "ctrl+s")
	out := cmd()
	m, join := catalogKey(m, "esc")
	m = catalogFinish(t, m, join)
	m.squads = append(m.squads, domain.Squad{ID: "other", Name: "Other", Version: 7})
	m.cursor = 1
	m, _ = catalogKey(m, "enter")
	m = catalogFinish(t, m, func() tea.Msg { return out })
	if m.editor.ID != "other" || m.editor.Version != 7 || len(m.catalog.receipts) != 1 || m.catalog.receipts[0].squad.ID != "saved-actual" {
		t.Fatal("late save selected its draft or lost receipt")
	}
}
func TestCatalogValidationMemberSwitchInvalidatesInFlight(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(client.SquadValidation{Validation: domain.Validation{Ready: true, Title: "stale member validation"}})
	})
	m := New(c).(model)
	m = m.startSquad(domain.Squad{Name: "Two", Members: []domain.Member{{ID: "one"}, {ID: "two"}}})
	m, cmd := catalogKey(m, "ctrl+v")
	out := cmd()
	m, _ = catalogKey(m, "ctrl+down")
	m = catalogFinish(t, m, func() tea.Msg { return out })
	if m.catalog.validation != nil {
		t.Fatal("member switch restored old validation")
	}
}
func TestCatalogSelectedMissionHeaderUsesImmutableSelection(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m := New(nil).(model)
	m.missions = []domain.Mission{{ID: "one", Title: "Selected objective", WorkspaceID: "w", SquadSnapshot: domain.Squad{ID: "sq", Version: 4, Members: []domain.Member{{ID: "original-member", Role: "Original role"}}}}}
	m.screen = scrMissions
	m, _ = catalogKey(m, "enter")
	m.missions[0].Title = "Foreign stale title"
	m.missions[0].SquadSnapshot.Members[0].Role = "Foreign stale role"
	view := m.View().Content
	if strings.Contains(view, "Foreign stale") || !strings.Contains(view, "Selected objective") || !strings.Contains(view, "Original role") {
		t.Fatal("room header/detail reads mutable catalog instead of selected snapshot: " + view)
	}
}
func TestCatalogProviderAccountSwitchKeepsOtherFields(t *testing.T) {
	m := New(nil).(model)
	m.accounts = []domain.Account{{ID: "cx", Adapter: "codex"}, {ID: "cl", Adapter: "claude"}}
	m = m.startSquad(domain.Squad{ID: "sq", Members: []domain.Member{{ID: "mb", Role: "Lead", CLI: "codex", AccountID: "cx", Model: "explicit-model", Effort: "high", Skills: []string{"one"}, Maestro: true}}})
	m = catalogTabs(m, 3)
	m.catalog.validation = &domain.Validation{Ready: true}
	m, _ = catalogKey(m, "right")
	member := m.editor.Members[0]
	if member.CLI != "claude" || member.AccountID != "" || member.Model != "explicit-model" || member.Effort != "high" || !reflect.DeepEqual(member.Skills, []string{"one"}) || !member.Maestro || member.ID != "mb" || m.catalog.validation != nil {
		t.Fatalf("switch %+v", member)
	}
	m = catalogTabs(m, 1)
	m, _ = catalogKey(m, "right")
	if m.editor.Members[0].AccountID != "cl" {
		t.Fatal("matching exact account not selected")
	}
}
func TestCatalogPTENFocusAndLocalFacts(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			m := catalogSimple(New(nil).(model))
			want := map[string][]string{"en": {"Squad editor", "Local draft; no saved identity", "ctrl+s save"}, "pt": {"Editor de squad", "Draft local; sem identidade salva", "ctrl+s salvar"}}[lang]
			for _, s := range want {
				accountText(t, m, s)
			}
			m.account.uncertain = &accountRequest{}
			m.workspace.uncertain = &workspaceRequest{}
			accountText(t, m, map[string]string{"en": "Esc leaves text focus; then c/w opens account/workspace recovery", "pt": "Esc sai do texto; depois c/w abre recuperação de conta/workspace"}[lang])
			m, _ = catalogKey(m, "esc")
			m, _ = catalogKey(m, "m")
			m, _ = catalogKey(m, "n")
			accountText(t, m, map[string]string{"en": "New mission", "pt": "Nova missão"}[lang])
			accountText(t, m, "ctrl+enter")
		})
	}
}
func TestCatalogOptionalBriefAndExplicitWorkspace(t *testing.T) {
	var body client.CreateMissionRequest
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(domain.Mission{ID: "actual", WorkspaceID: body.WorkspaceID, Title: body.Title, SquadID: body.SquadID, SquadSnapshot: domain.Squad{ID: body.SquadID, Version: 1}, Status: "open"})
	})
	m := New(c).(model)
	m.ws = []domain.Workspace{{ID: "first"}, {ID: "selected"}}
	m.squads = []domain.Squad{{ID: "s", Version: 1}}
	m = m.selectWorkspace(m.ws[1])
	m, _ = catalogKey(m, "esc")
	m, _ = catalogKey(m, "m")
	m, _ = catalogKey(m, "n")
	if m.catalog.mission.workspaceID != "selected" || m.catalog.mission.squad.ID != "" {
		t.Fatal("explicit workspace not reused or first squad selected")
	}
	m = catalogPaste(m, "Title only")
	m = catalogTabs(m, 3)
	m, _ = catalogKey(m, "right")
	m, _ = catalogKey(m, "ctrl+enter")
	m, cmd := catalogKey(m, "ctrl+enter")
	m = catalogFinish(t, m, cmd)
	if body != (client.CreateMissionRequest{WorkspaceID: "selected", SquadID: "s", Title: "Title only"}) || m.missionID != "actual" {
		t.Fatalf("optional brief contract %+v", body)
	}
}

func TestCatalogBackgroundReadDoesNotAuthorizeAcknowledgment(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, `{"error":"unknown"}`, 409)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	m := catalogSimple(New(c).(model))
	m, cmd := catalogKey(m, "ctrl+s")
	m = catalogFinish(t, m, cmd)
	m = catalogFinish(t, m, m.readCatalog())
	m, _ = catalogKey(m, "ctrl+d")
	if m.catalog.uncertain == nil {
		t.Fatal("automatic read acknowledged unknown mutation without explicit inspection")
	}
	m, cmd = catalogKey(m, "ctrl+f")
	m = catalogFinish(t, m, cmd)
	m, _ = catalogKey(m, "ctrl+d")
	if m.catalog.uncertain != nil {
		t.Fatal("explicit inspection could not acknowledge")
	}
}

func TestCatalogMissionSelectionPreservesActualUnrelatedPTY(t *testing.T) {
	m, manager := terminalAttachedFixture(t)
	m.link.offer(terminalAction{operation: "tui"})
	m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	link, id, generation := m.link, m.sessID, m.generation
	before, _ := manager.Snapshot(id)
	m.missions = []domain.Mission{{ID: "mission-one", WorkspaceID: "workspace-one", SquadSnapshot: domain.Squad{ID: "s1", Version: 1, Members: []domain.Member{{ID: "m1", AccountID: "a1"}}}}, {ID: "mission-two", WorkspaceID: "workspace-two", SquadSnapshot: domain.Squad{ID: "s2", Version: 2, Members: []domain.Member{{ID: "m2", AccountID: "a2"}}}}}
	var cmd tea.Cmd
	for i, mission := range m.missions {
		m, _ = catalogKey(m, "m")
		if i == 1 {
			m, _ = catalogKey(m, "down")
		}
		m, cmd = catalogKey(m, "enter")
		m = catalogFinish(t, m, cmd)
		actual, _ := manager.Snapshot(id)
		if m.link != link || m.sessID != id || m.generation != generation || actual.OwnerClient != before.OwnerClient || actual.Process != "running" || m.missionID != mission.ID || m.workspace.selected.ID != mission.WorkspaceID {
			t.Fatal("catalog navigation changed active terminal recipient/control")
		}
		accountText(t, m, mission.ID)
		accountText(t, m, mission.SquadSnapshot.Members[0].ID)
	}
	m.termMode = true
	m = catalogPaste(m, "CATALOG_CHILD_qnsvfd")
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "CATALOG_CHILD_qnsvfd") })
	other, _ := manager.Snapshot("a-unrelated")
	if other.Process != "running" || other.OwnerClient != "" {
		t.Fatal("unrelated child changed")
	}
}

func TestCatalogReadsJoinWhenLeavingOtherFocusedFlows(t *testing.T) {
	for _, screen := range []screen{scrAccount, scrWorkspace, scrHome, scrAccounts, scrWorkspaces} {
		t.Run(string(screen), func(t *testing.T) {
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("canceled read was dispatched")
				_, _ = w.Write([]byte(`[]`))
			})
			m := New(c).(model)
			m.workspace.refresh = nil
			m.account.read = nil
			m.screen = screen
			cmd := m.readCatalog()
			r := m.catalog.read
			key := "esc"
			if screen == scrHome || screen == scrAccounts || screen == scrWorkspaces {
				key = "n"
			}
			m, join := catalogKey(m, key)
			if r.ctx.Err() == nil || join == nil {
				t.Fatal("navigation left catalog request alive")
			}
			m = catalogFinish(t, m, join)
			m = catalogFinish(t, m, cmd)
			if m.catalog.read != nil {
				t.Fatal("navigation did not join catalog request")
			}
		})
	}
}

func TestCatalogForeignValidationCannotClaimCurrentDraftReady(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(client.SquadValidation{Validation: domain.Validation{Ready: true, Title: "Foreign validation"}, Squad: domain.Squad{ID: "foreign", Name: "someone else"}})
	})
	m := catalogSimple(New(c).(model))
	m.editor.ID = "selected"
	before := cloneSquad(m.editor)
	m, cmd := catalogKey(m, "ctrl+v")
	m = catalogFinish(t, m, cmd)
	if m.catalog.validation != nil || m.catalog.problem != "catalog_validation_failed" || !reflect.DeepEqual(m.editor, before) || m.catalog.uncertain != nil {
		t.Fatal("foreign validation became draft readiness")
	}
}
