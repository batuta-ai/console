package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyExplicitSessionInspectionAfterLostPlanningReply(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	msn := journeyMission("codex")
	count := 0
	actual := domain.Session{ID: "actual-session", MissionID: msn.ID, WorkspaceID: msn.WorkspaceID, MemberID: "lead-codex", AccountID: "account-codex", Adapter: "codex", Owner: "tui", OwnerClient: "client-exact", BatutaInjected: true}
	foreign := actual
	foreign.ID = "foreign-session"
	foreign.MissionID = "foreign"
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			count++
			w.WriteHeader(500)
			_, _ = w.Write([]byte("lost reply"))
			return
		}
		if r.URL.Path != "/v0/sessions" {
			t.Error(r.URL)
		}
		_ = json.NewEncoder(w).Encode([]domain.Session{foreign, actual})
	})
	m := model{api: c, clientID: "client-exact"}.selectMission(msn)
	m = journeyKey(t, m, "p")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "planning" {
		t.Fatal("lost reply not quarantined")
	}
	m = journeyKey(t, m, "p")
	m = journeyKey(t, m, "enter")
	m = journeyKey(t, m, "l")
	accountText(t, m, "actual-session")
	m = catalogPaste(m, actual.ID)
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().session == nil || m.journeyRecord().session.ID != actual.ID || m.journeyRecord().uncertain != "" || count != 1 || m.link != nil || m.termMode {
		t.Fatal("explicit inspection failed", m.View().Content)
	}
	m = journeyKey(t, m, "p")
	m = journeyKey(t, m, "enter")
	if count != 1 {
		t.Fatal("inspection allowed duplicate planning")
	}
}
func TestJourneyHistoricalReceiptNeverReplacesOriginalExecutionTarget(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	old := f.receipt
	old.ID = "historical-receipt"
	old.Content = []byte("old exact source")
	old.Identity.Version = domain.PlanVersion(old.Content)
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/approvals/") {
			_ = json.NewEncoder(w).Encode(old)
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "h")
	m = catalogPaste(m, old.ID)
	m = journeyKey(t, m, "enter")
	accountText(t, m, old.ID)
	if m.journeyRecord().receipt == nil || m.journeyRecord().receipt.ID != f.receipt.ID {
		t.Fatal("historical receipt retargeted execution")
	}
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if strings.Join(f.writes, ",") != "resume" {
		t.Fatal(f.writes)
	}
}
func TestJourneyUncertainApprovalRequiresExactExplicitInspection(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.detail.Approval = nil
	count := 0
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			count++
			f.detail.Approval = &f.receipt
			w.WriteHeader(500)
			_, _ = w.Write([]byte("lost receipt"))
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyInspectAll(t, m)
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "approve" {
		t.Fatal("uncertain approval lost")
	}
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	if m.journeyRecord().uncertain == "" {
		t.Fatal("ordinary refresh acknowledged uncertain mutation")
	}
	m = journeyKey(t, m, "o")
	if m.journeyRecord().uncertain != "" || m.journeyRecord().receipt == nil || m.journeyRecord().receipt.ID != f.receipt.ID || count != 1 {
		t.Fatal("exact explicit source/receipt inspection did not reconcile")
	}
}
func TestJourneyPlanningMissingAmbiguousAndForeignFailClosed(t *testing.T) {
	for _, kind := range []string{"missing", "ambiguous", "foreign", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			msn := journeyMission("claude")
			count := 0
			if kind == "missing" {
				msn.SquadSnapshot.Members = nil
			}
			if kind == "ambiguous" {
				msn.SquadSnapshot.Members = append(msn.SquadSnapshot.Members, msn.SquadSnapshot.Members[0])
			}
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				var req client.StartSessionRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if kind == "malformed" {
					_, _ = w.Write([]byte("{}"))
					return
				}
				_ = json.NewEncoder(w).Encode(domain.Session{ID: "foreign-confirmation", MissionID: "foreign", WorkspaceID: msn.WorkspaceID, MemberID: req.MemberID, AccountID: req.AccountID, Adapter: req.Adapter, Owner: "tui", OwnerClient: req.Client, BatutaInjected: true})
			})
			m := model{api: c, clientID: "exact-client"}.selectMission(msn)
			m = journeyKey(t, m, "p")
			m = journeyKey(t, m, "enter")
			if m.journeyRecord().session != nil {
				t.Fatal("foreign session adopted")
			}
			if kind == "missing" || kind == "ambiguous" {
				if count != 0 {
					t.Fatal("ambiguous start")
				}
			} else if count != 1 || m.journeyRecord().uncertain != "planning" || len(m.journeyRecord().outcomes) != 1 {
				t.Fatal("uncertainty not retained")
			}
		})
	}
}
