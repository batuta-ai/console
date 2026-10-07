package tui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyStartRechecksExactEditableSourceBeforeDispatch(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "e")
	f.detail.Content = []byte("changed after confirmation")
	f.detail.Identity.Version = domain.PlanVersion(f.detail.Content)
	f.detail.Approval = nil
	m = journeyKey(t, m, "enter")
	if len(f.writes) != 0 || m.journeyRecord().receipt.ID != f.receipt.ID || m.journeyRecord().fresh {
		t.Fatal("stale editable source dispatched start")
	}
}
func TestJourneyUncertainResumeInspectsRunningOriginalWithoutReplay(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	handler := f.handler(t)
	calls := 0
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/resume") {
			calls++
			f.state.Active = true
			w.WriteHeader(500)
			_, _ = w.Write([]byte("lost started result"))
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "resume" {
		t.Fatal("uncertain resume lost")
	}
	m = journeyFinish(t, m, m.readJourney("refresh"))
	if m.journeyRecord().uncertain == "" {
		t.Fatal("poll acknowledged resume")
	}
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "" || calls != 1 || !m.journeyRecord().state.Active {
		t.Fatal("explicit original-running inspection failed or replayed", m.View().Content)
	}
}
