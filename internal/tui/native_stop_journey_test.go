package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestJourneyNativeStopFrozenPreparationAndObservation(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.state.Active, f.state.RunID = true, strings.Repeat("a", 64)
	original := domain.ExecutionStopRequest{Target: f.state.Target, ApprovalID: f.state.ApprovalID, Delivery: f.state.Delivery, RunID: f.state.RunID}
	var bodies []domain.ExecutionStopRequest
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			var req domain.ExecutionStopRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, req)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopPending})
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "z")
	if m.journeyRecord().confirm == "" {
		t.Fatal("active preparation has no Stop confirmation")
	}
	if len(bodies) != 0 {
		t.Fatal("Stop before confirmation")
	}
	f.state.RunID = strings.Repeat("b", 64)
	m = journeyFinish(t, m, m.readJourney("refresh"))
	m, cmd := catalogKey(m, "enter")
	m, duplicate := catalogKey(m, "enter")
	if cmd == nil || duplicate != nil {
		t.Fatal("missing Stop or duplicate dispatch")
	}
	m = journeyFinish(t, m, cmd)
	m = journeyFinish(t, m, cmd)
	if !reflect.DeepEqual(bodies, []domain.ExecutionStopRequest{original}) {
		t.Fatal("retargeted or duplicate Stop", bodies)
	}
	accountText(t, m, "Cancellation pending")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "z")
	m = journeyKey(t, m, "enter")
	if len(bodies) != 1 {
		t.Fatal("pending Stop silently dispatched again")
	}
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if !reflect.DeepEqual(bodies, []domain.ExecutionStopRequest{original, original}) {
		t.Fatal("observation retargeted", bodies)
	}
}

func TestJourneyNativeStopMissingAuthority(t *testing.T) {
	for _, run := range []string{"", "invalid", strings.Repeat("A", 64)} {
		t.Run(run, func(t *testing.T) {
			f := newJourneyWire("codex")
			calls := 0
			m := model{api: accountFixture(t, func(http.ResponseWriter, *http.Request) { calls++ })}.selectMission(f.mission)
			r := m.journeyRecord()
			s := f.state
			s.Active = true
			s.RunID = run
			r.state = &s
			r.receipt = &f.receipt
			m.putJourney(r)
			m = journeyKey(t, m, "z")
			m = journeyKey(t, m, "enter")
			if m.journeyRecord().confirm != "" || calls != 0 {
				t.Fatal("invalid RunID authorized Stop")
			}
		})
	}
}

func TestJourneyNativeStopOutcomesRemainInspectable(t *testing.T) {
	for _, outcome := range []string{"completed", "uncertain", "malformed", "lost", "rejected"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", "en")
			f := newJourneyWire("codex")
			f.opened()
			f.state.Active = true
			f.state.RunID = strings.Repeat("a", 64)
			var bodies []domain.ExecutionStopRequest
			handler := f.handler(t)
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/stop") {
					var req domain.ExecutionStopRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					bodies = append(bodies, req)
					result := domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopEffect(outcome)}
					switch outcome {
					case "completed":
						state := f.state
						state.Active = false
						state.RunID = ""
						state.Watch.Header = map[string]any{"state": "blocked"}
						result.Finalized = true
						result.Durable = true
						result.CoreState = "blocked"
						result.State = &state
						result.Causes = []string{"run failed before finalization"}
					case "uncertain":
						result.Finalized = true
						result.Causes = []string{"journal write failed"}
						result.Error = &domain.PlanFailure{Code: "uncertain", Message: "durability unknown"}
						w.WriteHeader(503)
					case "rejected":
						result.Error = &domain.PlanFailure{Code: "stale", Message: "old attempt"}
						w.WriteHeader(409)
					case "malformed":
						_, _ = w.Write([]byte(`{"effect":"completed"}`))
						return
					case "lost":
						w.WriteHeader(502)
						_, _ = w.Write([]byte("lost response"))
						return
					}
					_ = json.NewEncoder(w).Encode(result)
					return
				}
				handler(w, r)
			})
			m := model{api: c}.selectMission(f.mission)
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "z")
			m = journeyKey(t, m, "enter")
			if len(bodies) != 1 {
				t.Fatal("Stop not sent", m.View().Content)
			}
			before := m.View().Content
			switch outcome {
			case "completed":
				if !strings.Contains(before, "blocked") || !strings.Contains(before, "run failed before finalization") {
					t.Fatal("actual completion missing", before)
				}
			case "uncertain":
				if !strings.Contains(before, "journal write failed") {
					t.Fatal("causes lost", before)
				}
			case "lost", "malformed":
				if !strings.Contains(before, "uncertain") {
					t.Fatal("unverified response not quarantined", before)
				}
			case "rejected":
				if !strings.Contains(before, "old attempt") {
					t.Fatal("rejection lost", before)
				}
			}
			f.state.RunID = strings.Repeat("b", 64)
			m = journeyKey(t, m, "f")
			m = journeyFinish(t, m, m.readJourney("refresh"))
			if len(bodies) != 1 || len(m.journeyRecord().outcomes) != 1 {
				t.Fatal("refresh replayed or lost history")
			}
			if outcome == "uncertain" && !strings.Contains(m.View().Content, "journal write failed") {
				t.Fatal("inspection erased Stop uncertainty")
			}
			if outcome == "uncertain" || outcome == "lost" || outcome == "malformed" {
				m = journeyKey(t, m, "z")
				m = journeyKey(t, m, "enter")
				if len(bodies) != 1 {
					t.Fatal("unknown submission re-enabled dispatch confirmation")
				}
				m = journeyKey(t, m, "y")
				m = journeyKey(t, m, "enter")
				if len(bodies) != 2 || bodies[0] != bodies[1] {
					t.Fatal("explicit observation did not retain exact request")
				}
			}
		})
	}
}

func TestJourneyNativeStopRetainedAfterReadFailure(t *testing.T) {
	f := newJourneyWire("codex")
	f.state.Active = true
	f.state.RunID = strings.Repeat("a", 64)
	handler := f.handler(t)
	calls := 0
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			calls++
			var req domain.ExecutionStopRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopPending})
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "f")
	f.failRead = true
	m = journeyKey(t, m, "f")
	if m.journeyRecord().fresh {
		t.Fatal("failed read kept general mutation authority")
	}
	m = journeyKey(t, m, "z")
	m = journeyKey(t, m, "enter")
	if calls != 1 {
		t.Fatal("review read failure blocked exact Stop")
	}
}

func TestJourneyNativeStopLateCompletionStaysWithOriginalAttempt(t *testing.T) {
	for _, navigation := range []string{"replacement", "mission", "workspace", "epoch"} {
		t.Run(navigation, func(t *testing.T) {
			f := newJourneyWire("codex")
			f.state.Active = true
			f.state.RunID = strings.Repeat("a", 64)
			handler := f.handler(t)
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/stop") {
					var req domain.ExecutionStopRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					state := f.state
					state.Active = false
					state.RunID = ""
					_ = json.NewEncoder(w).Encode(domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopCompleted, Finalized: true, Durable: true, State: &state})
					return
				}
				handler(w, r)
			})
			m := model{api: c}.selectMission(f.mission)
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "z")
			m, cmd := catalogKey(m, "enter")
			if cmd == nil {
				t.Fatal("missing Stop")
			}
			// Complete real client I/O, but deliver its message only after navigation/replacement.
			msg := cmd()
			originalKey := journeyID(f.mission)
			r := m.journeyRecord()
			replacement := *r.state
			replacement.RunID = strings.Repeat("b", 64)
			r.state = &replacement
			m.putJourney(r)
			switch navigation {
			case "mission":
				m = m.selectMission(journeyMission("claude"))
			case "workspace":
				foreign := f.mission
				foreign.WorkspaceID = "other-workspace"
				m = m.selectMission(foreign)
			case "epoch":
				m.journey.epoch++
			}
			next, _ := m.Update(msg)
			m = next.(model)
			original := m.journey.records[originalKey]
			if original.state.RunID != replacement.RunID || !original.state.Active || len(original.outcomes) != 1 {
				t.Fatal("late completion overwrote replacement or lost history")
			}
			if navigation == "mission" || navigation == "workspace" {
				if m.journeyRecord().state != nil || len(m.journeyRecord().outcomes) != 0 || strings.Contains(m.View().Content, "Stop finalized durably") {
					t.Fatal("foreign outcome leaked")
				}
			}
			m = m.selectMission(f.mission)
			if len(m.journeyRecord().outcomes) != 1 {
				t.Fatal("history lost on return")
			}
		})
	}
}

func TestJourneyNativeStopPreservesOtherOperationUncertainty(t *testing.T) {
	for _, kind := range []string{"answer", "start", "resume"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", "en")
			f := newJourneyWire("codex")
			f.opened()
			f.state.Active = true
			f.state.RunID = strings.Repeat("a", 64)
			handler := f.handler(t)
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/stop") {
					var req domain.ExecutionStopRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					state := f.state
					state.Active = false
					state.RunID = ""
					state.Watch.Header = map[string]any{"state": "blocked"}
					_ = json.NewEncoder(w).Encode(domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopCompleted, Finalized: true, Durable: true, CoreState: "blocked", State: &state})
					return
				}
				handler(w, r)
			})
			m := model{api: c}.selectMission(f.mission)
			m = journeyKey(t, m, "f")
			r := m.journeyRecord()
			r.uncertain = kind
			r.problem = "journey_uncertain"
			r.problemKind = kind
			r.problemErr = fmt.Errorf("original %s unknown", kind)
			req := &journeyRequest{kind: kind, key: journeyID(f.mission)}
			failure := journeyResult{request: req, effect: "uncertain", err: r.problemErr, execution: domain.ExecutionResult{Target: f.state.Target, ApprovalID: f.state.ApprovalID}}
			r.outcomes = []journeyResult{failure}
			r.mutationFailure = &failure
			m.putJourney(r)
			m = journeyKey(t, m, "z")
			m = journeyKey(t, m, "enter")
			if m.journeyRecord().uncertain != kind || m.journeyRecord().mutationFailure != &failure || !strings.Contains(m.View().Content, "original "+kind+" unknown") {
				t.Fatal("Stop acknowledged another operation", m.View().Content)
			}
			m = journeyFinish(t, m, m.readJourney("refresh"))
			if m.journeyRecord().uncertain != kind {
				t.Fatal("poll acknowledged other operation")
			}
			m = journeyKey(t, m, "f")
			if kind == "answer" && m.journeyRecord().uncertain != "answer" {
				t.Fatal("inspection fabricated answer acknowledgment")
			}
			if kind != "answer" && m.journeyRecord().uncertain != "" {
				t.Fatal("Stop history broke explicit original-running inspection")
			}

		})
	}
}

func TestJourneyNativeStopTranslationsAndParkedQuestion(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			f := newJourneyWire("codex")
			f.opened()
			f.question(1)
			m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "z")
			if m.journeyRecord().confirm != "" {
				t.Fatal("parked question has no native attempt to Stop")
			}
			text := m.View().Content
			expected := "z native Stop  y reobserve exact Stop"
			if lang == "pt" {
				expected = "z Stop nativo  y reobservar Stop exato"
			}
			if !strings.Contains(text, expected) {
				t.Fatal("missing translated help", text)
			}
		})
	}
}

func TestJourneyNativeStopCanceledBeforeDispatch(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.state.Active = true
	f.state.RunID = strings.Repeat("a", 64)
	calls := 0
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			calls++
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "z")
	m, cmd := catalogKey(m, "enter")
	if cmd == nil {
		t.Fatal("missing Stop")
	}
	m.journey.pending.cancel()
	m = journeyFinish(t, m, cmd)
	if calls != 0 || !strings.Contains(m.View().Content, "Stop was not sent") {
		t.Fatal("pre-dispatch cancellation misreported", m.View().Content)
	}
	m = journeyKey(t, m, "z")
	if m.journeyRecord().confirm != "stop" {
		t.Fatal("explicit recovery unavailable after not-sent")
	}
}
