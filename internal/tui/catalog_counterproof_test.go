package tui

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"net/http"
	"strings"
	"testing"
)

func conductorMission(m model) model {
	m.ws = []domain.Workspace{{ID: "explicit-workspace"}}
	m.squads = []domain.Squad{{ID: "explicit-squad", Version: 2}}
	m.screen = scrMissions
	m, _ = catalogKey(m, "n")
	m = catalogPaste(m, "Exact objective")
	m = catalogTabs(m, 1)
	m = catalogPaste(m, "Exact brief")
	m = catalogTabs(m, 1)
	m, _ = catalogKey(m, "right")
	m = catalogTabs(m, 1)
	m, _ = catalogKey(m, "right")
	return m
}
func TestConductorMissionLegacyEnterReviewsAndConfirms(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var writes int
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writes++
		var in client.CreateMissionRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		want := client.CreateMissionRequest{WorkspaceID: "explicit-workspace", SquadID: "explicit-squad", Title: "Exact objective", Brief: "Exact brief"}
		if in != want {
			t.Errorf("body differs: %+v", in)
		}
		_ = json.NewEncoder(w).Encode(domain.Mission{ID: "actual-mission", WorkspaceID: in.WorkspaceID, SquadID: in.SquadID, Title: in.Title, Brief: in.Brief, Status: "open", SquadSnapshot: domain.Squad{ID: in.SquadID, Version: 2}})
	})
	m := conductorMission(New(c).(model))
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || writes != 0 || !m.catalog.mission.reviewing {
		t.Fatal("plain Enter on chosen squad cannot enter review without enhanced keyboard")
	}
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("plain Enter cannot confirm reviewed mission")
	}
	m = catalogFinish(t, m, cmd)
	if writes != 1 || m.missionID != "actual-mission" {
		t.Fatal("confirmed exact mission not created once")
	}
}
func TestConductorMissionRejectsForeignObjectiveReply(t *testing.T) {
	for _, field := range []string{"title", "brief"} {
		t.Run(field, func(t *testing.T) {
			var writes int
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				writes++
				var in client.CreateMissionRequest
				_ = json.NewDecoder(r.Body).Decode(&in)
				out := domain.Mission{ID: "unverified-mission", WorkspaceID: in.WorkspaceID, SquadID: in.SquadID, Title: in.Title, Brief: in.Brief, Status: "open", SquadSnapshot: domain.Squad{ID: in.SquadID, Version: 2}}
				if field == "title" {
					out.Title = "Someone else's objective"
				} else {
					out.Brief = "Someone else's requirements"
				}
				_ = json.NewEncoder(w).Encode(out)
			})
			m := conductorMission(New(c).(model))
			m, _ = catalogKey(m, "ctrl+enter")
			m, cmd := catalogKey(m, "ctrl+enter")
			m = catalogFinish(t, m, cmd)
			if writes != 1 || m.missionID != "" || m.catalog.uncertain == nil || m.catalog.mission.title != "Exact objective" || m.catalog.mission.brief != "Exact brief" {
				t.Fatal("foreign objective reply became confirmed mission or discarded draft")
			}
			m, cmd = catalogKey(m, "ctrl+enter")
			if cmd != nil || writes != 1 {
				t.Fatal("unknown create replayed")
			}
		})
	}
}
func TestConductorCatalogTextArrowKeepsFreshValidation(t *testing.T) {
	m := catalogSimple(New(nil).(model))
	m.catalog.validation = &domain.Validation{Ready: true, Title: "exact draft"}
	revision := m.catalog.revision
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = next.(model)
	if m.catalog.validation == nil || m.catalog.revision != revision || !strings.Contains(m.editor.Name, "Kept draft") {
		t.Fatal("no-op arrow invalidated unchanged draft validation")
	}
}
