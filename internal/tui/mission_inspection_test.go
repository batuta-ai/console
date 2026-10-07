package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyUncertainBindingInspectsExactPersistedMission(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.detail.Approval = nil
	selected := f.mission
	selected.Binding = nil
	writes := 0
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			writes++
			w.WriteHeader(500)
			_, _ = w.Write([]byte("lost bind response"))
			return
		}
		if r.URL.Path == "/v0/missions" {
			if r.URL.Query().Get("workspace") != selected.WorkspaceID {
				t.Error("wrong catalog inspection")
			}
			_ = json.NewEncoder(w).Encode([]domain.Mission{journeyMission("foreign"), f.mission})
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(selected)
	m = journeyKey(t, m, "b")
	m = catalogPaste(m, f.detail.Identity.Binding.PlanPath)
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "enter")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "bind" {
		t.Fatal("missing uncertainty")
	}
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	r := m.journeyRecord()
	if r.uncertain != "" || r.mission.Binding == nil || *r.mission.Binding != f.detail.Identity.Binding || writes != 1 {
		t.Fatal("exact bind inspection failed", m.View().Content)
	}
	m = journeyKey(t, m, "o")
	accountText(t, m, f.detail.Identity.Version)
}
func TestJourneyRefreshPreservesFocusedSourceUntilExplicitReload(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.detail.Approval = nil
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyInspectAll(t, m)
	shown := m.journeyRecord().detail.Identity
	f.detail.Content = []byte("externally changed content")
	f.detail.Identity.Version = domain.PlanVersion(f.detail.Content)
	m = journeyFinish(t, m, m.readJourney("refresh"))
	if m.journeyRecord().detail.Identity != shown {
		t.Fatal("poll silently replaced inspected source")
	}
	m = journeyKey(t, m, "o")
	if m.journeyRecord().detail.Identity != f.detail.Identity || m.journeyRecord().inspected >= len(journeySourceLines(f.detail)) {
		t.Fatal("explicit reload failed to reset inspection")
	}
}
func TestJourneyPollingCannotReauthorizeSourceAfterReadFailure(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.detail.Approval = nil
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyInspectAll(t, m)
	f.failRead = true
	m = journeyKey(t, m, "o")
	f.failRead = false
	m = journeyFinish(t, m, m.readJourney("refresh"))
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if len(f.writes) != 0 {
		t.Fatal("background poll authorized stale approval")
	}
}
func TestJourneyAnswerUncertaintyAllowsOnlyInspectedOriginalResume(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	f.question(1)
	handler := f.handler(t)
	answers := 0
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/answer") {
			answers++
			f.state.Watch.PendingQuestion = nil
			w.WriteHeader(500)
			_, _ = w.Write([]byte("reply lost after recording"))
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "j")
	m = catalogPaste(m, "preserved answer")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "enter")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "answer" {
		t.Fatal("uncertain answer discarded")
	}
	m = journeyKey(t, m, "esc")
	m = journeyFinish(t, m, m.readJourney("refresh"))
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if len(f.writes) != 0 {
		t.Fatal("poll cleared unknown answer")
	}
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if strings.Join(f.writes, ",") != "resume" || answers != 1 || m.journeyRecord().answer != "preserved answer" {
		t.Fatal("explicit inspected resume missing", f.writes, m.View().Content)
	}
	// The original answer remains quarantined; no acknowledgment pretends it succeeded.
	if m.journeyRecord().uncertain != "answer" {
		t.Fatal("inspection fabricated recorded answer")
	}
}
