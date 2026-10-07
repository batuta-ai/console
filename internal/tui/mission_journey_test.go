package tui

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func journeyFinish(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			m = journeyFinish(t, m, child)
		}
		return m
	}
	next, more := m.Update(msg)
	return journeyFinish(t, next.(model), more)
}
func journeyKey(t *testing.T, m model, key string) model {
	t.Helper()
	next, cmd := catalogKey(m, key)
	return journeyFinish(t, next, cmd)
}
func journeyMission(provider string) domain.Mission {
	return domain.Mission{ID: "mission-" + provider, WorkspaceID: "workspace-" + provider, SquadSnapshot: domain.Squad{ID: "snapshot-" + provider, Version: 3, Members: []domain.Member{{ID: "lead-" + provider, CLI: provider, AccountID: "account-" + provider, Maestro: true}}}}
}
func TestJourneyPlanningKeyboardExactMission(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			msn := journeyMission(provider)
			count := 0
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v0/sessions" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.Error(w, "unexpected", 500)
					return
				}
				count++
				var in client.StartSessionRequest
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				if in.MissionID != msn.ID || in.MemberID != "lead-"+provider || in.AccountID != "account-"+provider || in.Adapter != provider || in.Inject == nil || !*in.Inject || in.Owner != "tui" || in.Client != "exact-client" || len(in.Argv) != 0 {
					t.Errorf("wrong planning body %+v", in)
				}
				_ = json.NewEncoder(w).Encode(domain.Session{ID: "real-session", MissionID: msn.ID, WorkspaceID: msn.WorkspaceID, MemberID: in.MemberID, AccountID: in.AccountID, Adapter: provider, Owner: "tui", OwnerClient: in.Client, BatutaInjected: true})
			})
			m := model{api: c, clientID: "exact-client", screen: scrMissions, missions: []domain.Mission{journeyMission("foreign"), msn}, cursor: 1}
			m = journeyKey(t, m, "enter")
			accountText(t, m, "p interactive planning")
			m = journeyKey(t, m, "p")
			accountText(t, m, "lead-"+provider)
			if count != 0 {
				t.Fatal("planning before confirmation")
			}
			m = journeyKey(t, m, "enter")
			accountText(t, m, "real-session")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "p")
			m = journeyKey(t, m, "enter")
			if count != 1 || m.termMode || m.link != nil {
				t.Fatalf("duplicate or implicit attach: %d", count)
			}
		})
	}
}
func TestJourneyBindingSourceApprovalKeyboard(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	msn := journeyMission("codex")
	content := append([]byte("first\r\n"), 0xff, 0x1b)
	content = append(content, []byte(strings.Repeat("large line\n", 60)+"END-SOURCE")...)
	binding := domain.MissionBinding{WorkspaceID: msn.WorkspaceID, WorkspaceRoot: "/actual", CanonicalRoot: "/actual", PlanPath: ".batuta/plans/exact.md", PlanSlug: "exact", PlanDigest: "native-tasks"}
	id := domain.PlanIdentity{MissionID: msn.ID, Binding: binding, Version: domain.PlanVersion(content)}
	detail := domain.PlanDetail{Identity: id, Content: content, Title: "Exact", Tasks: []domain.PlanTask{{ID: "Task33", Accept: []string{"all bytes"}}}}
	writes := []string{}
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT":
			var in client.MissionBindingRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.WorkspaceID != msn.WorkspaceID || in.PlanPath != binding.PlanPath || in.DeliveryID != "" {
				t.Errorf("binding %+v", in)
			}
			writes = append(writes, "bind")
			bound := msn
			bound.Binding = &binding
			_ = json.NewEncoder(w).Encode(bound)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/plan"):
			if r.URL.Query().Get("workspace_id") != msn.WorkspaceID {
				t.Error("wrong workspace")
			}
			_ = json.NewEncoder(w).Encode(detail)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/approvals"):
			var got domain.PlanIdentity
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got != id {
				t.Errorf("approval target %+v", got)
			}
			writes = append(writes, "approve")
			_ = json.NewEncoder(w).Encode(domain.PlanApprovalResult{Target: id, Effect: domain.PlanApprovalRecorded, Receipt: &domain.PlanApproval{ID: "receipt-exact", Identity: id, Content: content, ApprovedAt: time.Unix(10, 0)}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	})
	m := model{api: c, clientID: "exact-client"}.selectMission(msn)
	accountText(t, m, "b bind existing plan")
	m = journeyKey(t, m, "b")
	m = catalogPaste(m, binding.PlanPath)
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "enter")
	if len(writes) != 0 {
		t.Fatal("binding before review confirmation")
	}
	m = journeyKey(t, m, "enter")
	accountText(t, m, binding.PlanPath)
	m = journeyKey(t, m, "o")
	accountText(t, m, id.Version)
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if len(writes) != 1 {
		t.Fatal("unread source approved")
	}
	shown := ""
	for range 200 {
		shown += m.View().Content
		if strings.Contains(m.View().Content, "END OF SOURCE") {
			break
		}
		m = journeyKey(t, m, "down")
	}
	if !strings.Contains(shown, `\r\n`) || !strings.Contains(shown, `\xff\x1b`) || !strings.Contains(shown, "END-SOURCE") {
		t.Fatalf("bytes lost: %s", shown)
	}
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if !reflect.DeepEqual(writes, []string{"bind", "approve"}) {
		t.Fatalf("writes %v", writes)
	}
	accountText(t, m, "receipt-exact")
}
