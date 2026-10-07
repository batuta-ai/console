package tui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyUnknownSessionConfirmationIsVisibleButNotAuthority(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	msn := journeyMission("codex")
	calls := 0
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(domain.Session{ID: "unconfirmed-real-id", MissionID: msn.ID, WorkspaceID: "foreign-workspace", MemberID: "lead-codex", AccountID: "account-codex", Adapter: "codex", Owner: "tui", OwnerClient: "actual-client", BatutaInjected: true})
	})
	m := model{api: c, clientID: "actual-client", sessID: "separate-terminal"}.selectMission(msn)
	m = journeyKey(t, m, "p")
	m = journeyKey(t, m, "enter")
	accountText(t, m, "Unconfirmed response; inspect explicitly")
	accountText(t, m, "unconfirmed-real-id")
	if m.journeyRecord().session != nil || m.journeyRecord().uncertain != "planning" || m.sessID != "separate-terminal" || m.link != nil || calls != 1 {
		t.Fatal("unconfirmed response gained authority")
	}
}
