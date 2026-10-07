package tui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyDoneRequiresFreshClearedGateAndFullEvidence(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	f.state.Watch.Detail = map[string]any{"last": strings.Repeat("exact evidence ", 20) + "END-EVIDENCE"}
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	accountText(t, m, "Completion is not verified")
	f.state.Gate.Cleared = true
	f.state.Gate.ReviewID = "review-exact"
	f.state.Gate.EvidenceDigest = "sha256:" + strings.Repeat("b", 64)
	f.state.Watch.Header["state"] = "done"
	m = journeyKey(t, m, "f")
	accountText(t, m, asciiFold("Done — current cleared gate and evidence"))
	m = journeyKey(t, m, "w")
	shown := ""
	for range 100 {
		shown += m.View().Content
		if strings.Contains(m.View().Content, "END OF SOURCE") {
			break
		}
		m = journeyKey(t, m, "down")
	}
	if !strings.Contains(shown, "END-EVIDENCE") || !strings.Contains(shown, "review-exact") {
		t.Fatal("evidence truncated", shown)
	}
	m = journeyKey(t, m, "esc")
	f.failRead = true
	m = journeyKey(t, m, "f")
	if strings.Contains(m.View().Content, asciiFold("Done — current cleared gate and evidence")) {
		t.Fatal("stale completion claim")
	}
	accountText(t, m, "review-exact")
}
func TestJourneyTypedRejectionsAndUncertainEffects(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, kind := range []string{"approve", "start", "resume", "answer"} {
		for _, status := range []int{400, 409, 500} {
			for _, effect := range []string{"rejected", "uncertain"} {
				t.Run(kind+"/"+http.StatusText(status)+"/"+effect, func(t *testing.T) {
					f := newJourneyWire("codex")
					if kind == "resume" || kind == "answer" {
						f.opened()
					}
					if kind == "answer" {
						f.question(1)
					}
					if kind == "approve" {
						f.detail.Approval = nil
					}
					calls := 0
					handler := f.handler(t)
					c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
						if r.Method != "POST" {
							handler(w, r)
							return
						}
						calls++
						w.WriteHeader(status)
						fail := &domain.PlanFailure{Code: "typed_refusal", Message: "exact rejected effect"}
						switch kind {
						case "approve":
							_ = json.NewEncoder(w).Encode(domain.PlanApprovalResult{Target: f.receipt.Identity, Effect: domain.PlanApprovalEffect(effect), Error: fail})
						case "start", "resume":
							_ = json.NewEncoder(w).Encode(domain.ExecutionResult{Target: f.receipt.Identity, ApprovalID: f.receipt.ID, Effect: domain.ExecutionEffect(effect), Error: fail})
						case "answer":
							_ = json.NewEncoder(w).Encode(domain.AnswerResult{Target: f.state.Watch.PendingQuestion.Target, Effect: domain.AnswerEffect(effect), Error: &domain.AnswerFailure{Code: "typed_refusal", Message: "exact rejected effect"}})
						}
					})
					m := model{api: c}.selectMission(f.mission)
					m = journeyKey(t, m, "o")
					key := "y"
					if kind == "approve" {
						m = journeyInspectAll(t, m)
					} else {
						m = journeyKey(t, m, "esc")
						m = journeyKey(t, m, "f")
						key = map[string]string{"start": "e", "resume": "g", "answer": "j"}[kind]
					}
					m = journeyKey(t, m, key)
					if kind == "answer" {
						m = catalogPaste(m, "retained\nanswer")
						m = journeyKey(t, m, "tab")
						m = journeyKey(t, m, "enter")
					}
					m = journeyKey(t, m, "enter")
					r := m.journeyRecord()
					if calls != 1 || r.effect != effect || (effect == "uncertain" && r.uncertain != kind) || (effect == "rejected" && r.uncertain != "") {
						t.Fatal("typed effect misclassified", calls, r.effect, r.uncertain)
					}
					if kind == "answer" && (r.answer != "retained\nanswer" || r.question.Target != f.state.Watch.PendingQuestion.Target) {
						t.Fatal("answer draft lost")
					}
					if effect == "uncertain" {
						m = journeyKey(t, m, key)
						m = journeyKey(t, m, "enter")
						if calls != 1 {
							t.Fatal("uncertain replay")
						}
					}
				})
			}
		}
	}
}
