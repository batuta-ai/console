package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

type journeyWire struct {
	mission  domain.Mission
	detail   domain.PlanDetail
	receipt  domain.PlanApproval
	state    domain.ExecutionState
	writes   []string
	answers  []domain.AnswerRequest
	failRead bool
}

func newJourneyWire(provider string) *journeyWire {
	m := journeyMission(provider)
	content := []byte("original\r\nsource\xff\n")
	b := domain.MissionBinding{WorkspaceID: m.WorkspaceID, WorkspaceRoot: "/fixture", CanonicalRoot: "/fixture", PlanPath: ".batuta/plans/exact.md", PlanSlug: "exact", PlanDigest: "native-tasks"}
	m.Binding = &b
	id := domain.PlanIdentity{MissionID: m.ID, Binding: b, Version: domain.PlanVersion(content)}
	a := domain.PlanApproval{ID: "receipt-" + provider, Identity: id, Content: content, ApprovedAt: time.Unix(20, 0)}
	return &journeyWire{mission: m, receipt: a, detail: domain.PlanDetail{Identity: id, Content: content, Approval: &a}, state: domain.ExecutionState{Target: id, ApprovalID: a.ID, Delivery: domain.ExecutionDelivery(a.ID, id)}}
}
func (f *journeyWire) opened() {
	f.state.OpenedDigest = "sha256:" + strings.Repeat("a", 64)
	f.state.Bound = true
	f.state.Gate = &domain.ExecutionGate{Required: true, Delivery: f.state.Delivery, Reason: "waiting for review"}
	f.state.Watch = &domain.WatchView{MissionID: f.mission.ID, WorkspaceID: f.mission.WorkspaceID, Delivery: f.state.Delivery, Source: domain.SourceCurrent, Progression: f.state.Gate, Header: map[string]any{"state": "waiting"}}
}
func (f *journeyWire) question(execution int) {
	f.state.Watch.PendingQuestion = &domain.PendingQuestion{Target: domain.QuestionIdentity{MissionID: f.mission.ID, WorkspaceID: f.mission.WorkspaceID, Delivery: f.state.Delivery, Task: "Task33", Execution: execution, QuestionID: "question-exact", OpenedDigest: f.state.OpenedDigest}, Prompt: "exact question"}
}
func (f *journeyWire) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && f.failRead {
			http.Error(w, "unavailable", 503)
			return
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/plan"):
			_ = json.NewEncoder(w).Encode(f.detail)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/approvals/"):
			_ = json.NewEncoder(w).Encode(f.receipt)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/execution"):
			if r.URL.Query().Get("approval_id") != f.receipt.ID || r.URL.Query().Get("workspace_id") != f.mission.WorkspaceID {
				t.Error("foreign state request", r.URL)
			}
			_ = json.NewEncoder(w).Encode(f.state)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/watch"):
			delivery := r.URL.Query().Get("delivery")
			if r.URL.Query().Get("workspace") != f.mission.WorkspaceID || (delivery != "" && delivery != f.state.Delivery) {
				t.Error("foreign watch", r.URL)
			}
			watch := domain.WatchView{MissionID: f.mission.ID, WorkspaceID: f.mission.WorkspaceID, Delivery: delivery, Source: domain.SourceCurrent}
			if f.state.Watch != nil {
				watch = *f.state.Watch
			}
			_ = json.NewEncoder(w).Encode(watch)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/approvals"):
			f.writes = append(f.writes, "approve")
			_ = json.NewEncoder(w).Encode(domain.PlanApprovalResult{Target: f.receipt.Identity, Effect: domain.PlanApprovalRecorded, Receipt: &f.receipt})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/execution"):
			var req domain.ExecutionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Target != f.receipt.Identity || req.ApprovalID != f.receipt.ID {
				t.Error("wrong original start", req)
			}
			f.writes = append(f.writes, "start")
			f.opened()
			f.state.Active = true
			f.state.RunID = strings.Repeat("a", 64)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(domain.ExecutionResult{RunID: f.state.RunID, Target: req.Target, ApprovalID: req.ApprovalID, Effect: domain.ExecutionStarted, Delivery: f.state.Delivery, OpenedDigest: f.state.OpenedDigest})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/resume"):
			var req domain.ResumeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.Target != f.receipt.Identity || req.ApprovalID != f.receipt.ID || req.Delivery != f.state.Delivery || req.OpenedDigest != f.state.OpenedDigest {
				t.Error("wrong original resume", req)
			}
			f.writes = append(f.writes, "resume")
			f.state.Active = true
			f.state.RunID = strings.Repeat("b", 64)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(domain.ExecutionResult{RunID: f.state.RunID, Target: req.Target, ApprovalID: req.ApprovalID, Effect: domain.ExecutionStarted, Delivery: f.state.Delivery, OpenedDigest: f.state.OpenedDigest})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/answer"):
			var req domain.AnswerRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if f.state.Watch.PendingQuestion == nil || req.Identity() != f.state.Watch.PendingQuestion.Target {
				t.Error("stale answer dispatched", req)
			}
			f.answers = append(f.answers, req)
			f.writes = append(f.writes, "answer")
			f.state.Watch.PendingQuestion = nil
			_ = json.NewEncoder(w).Encode(domain.AnswerResult{Target: req.Identity(), Delivery: req.Delivery, Effect: domain.AnswerRecorded})
		default:
			t.Error("unexpected request", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}
}
func journeyInspectAll(t *testing.T, m model) model {
	t.Helper()
	for range 2000 {
		if strings.Contains(m.View().Content, "END OF SOURCE") {
			return m
		}
		m = journeyKey(t, m, "down")
	}
	t.Fatal("source end unreachable")
	return m
}
func TestJourneyBoundSnapshotColdRecovery(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	f.question(1)
	b := *f.mission.Binding
	b.DeliveryID = f.state.Delivery
	b.OpenedDigest = f.state.OpenedDigest
	f.mission.Binding = &b
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	accountText(t, m, "Original approved snapshot")
	accountText(t, m, "Current editable file bytes are unavailable")
	m = journeyInspectAll(t, m)
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if len(f.writes) != 0 {
		t.Fatal("native snapshot reapproved", f.writes)
	}
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	if m.journeyRecord().state == nil || m.journeyRecord().state.OpenedDigest != f.state.OpenedDigest {
		t.Fatal("opening not recovered")
	}
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if len(f.writes) != 0 {
		t.Fatal("resumed unanswered question")
	}
}
func TestJourneySourceReadCannotRestoreFailedExecutionAuthority(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	f.question(1)
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	f.failRead = true
	m = journeyKey(t, m, "f")
	f.failRead = false
	// A historical receipt is independently readable; it cannot grant execution authority.
	m = journeyKey(t, m, "k")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "g")
	m = journeyKey(t, m, "enter")
	if len(f.writes) != 0 {
		t.Fatal("stale state authorized resume")
	}
}
func TestJourneyKeyboardStartAnswerResumeOriginalOpening(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			f := newJourneyWire(provider)
			m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
			m = journeyKey(t, m, "o")
			m = journeyKey(t, m, "esc")
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "e")
			m = journeyKey(t, m, "enter")
			if len(f.writes) != 1 || f.writes[0] != "start" {
				t.Fatal("no exact start", f.writes, m.View().Content)
			}
			f.question(1)
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "j")
			m = catalogPaste(m, "first\nanswer q f p")
			accountText(t, m, `first\nanswer q f p`)
			// Journal replacement while active, without any event; entered text stays pinned.
			f.question(2)
			m = journeyFinish(t, m, m.readJourney("refresh"))
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "enter")
			if len(f.answers) != 0 || m.journeyRecord().answer != "first\nanswer q f p" || m.journeyRecord().question.Target.Execution != 1 {
				t.Fatal("silently retargeted draft")
			}
			m = journeyKey(t, m, "esc")
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "n")
			m = journeyKey(t, m, "enter")
			m = catalogPaste(m, "second\nexact answer")
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "enter")
			if len(f.answers) != 1 || f.answers[0].Execution != 2 || f.answers[0].Text != "second\nexact answer" {
				t.Fatal("new exact answer not recorded", f.answers, m.View().Content)
			}
			if len(f.writes) != 2 {
				t.Fatal("automatic resume", f.writes)
			}
			f.state.Active = false
			f.state.RunID = ""
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "g")
			m = journeyKey(t, m, "enter")
			if strings.Join(f.writes, ",") != "start,answer,resume" {
				t.Fatal("wrong delivery actions", f.writes)
			}
		})
	}
}
