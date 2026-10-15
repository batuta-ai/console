package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

func TestJourneyLocalValidationPreservesMutationNotice(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, validation := range []string{"planning_identity", "changed_question", "planning_confirmation_identity"} {
		t.Run(validation, func(t *testing.T) {
			m := backgroundRefreshModel(t)
			r := m.journeyRecord()
			r.mode = ""
			r.mission.SquadSnapshot.Members = nil
			m.putJourney(r)
			req := &journeyRequest{kind: "start", key: journeyID(r.mission), mission: r.mission}
			cause := errors.New("credential_missing: original refusal")
			m.journey.pending = req
			next, cmd := m.Update(journeyResult{request: req, effect: "rejected", err: cause})
			if cmd != nil {
				t.Fatal("refusal scheduled another operation")
			}
			m = next.(model)
			before := m.journeyRecord()
			accountText(t, m, "start: credential_missing: original refusal")
			key, hint := "p", "journey_identity"
			if validation == "changed_question" {
				r = before
				r.fresh = true
				r.state = &domain.ExecutionState{Watch: &domain.WatchView{PendingQuestion: &domain.PendingQuestion{Prompt: "original question"}}}
				m.putJourney(r)
				m, cmd = catalogKey(m, "n")
				if cmd != nil || m.journeyRecord().confirm != "new_answer" {
					t.Fatal("question confirmation not opened locally")
				}
				r = m.journeyRecord()
				r.fresh = false
				m.putJourney(r)
				key, hint = "enter", "journey_question_changed"
			}
			if validation == "planning_confirmation_identity" {
				r = before
				r.mission.SquadSnapshot.Members = journeyMission("codex").SquadSnapshot.Members
				m.putJourney(r)
				m, cmd = catalogKey(m, "p")
				if cmd != nil || m.journeyRecord().confirm != "planning" {
					t.Fatal("planning confirmation not opened locally")
				}
				r = m.journeyRecord()
				r.mission.SquadSnapshot.Members = nil
				m.putJourney(r)
				key = "enter"
			}
			wantConfirm := ""
			if validation == "planning_confirmation_identity" {
				wantConfirm = "planning"
			}
			context := m.journeyRecord()
			m, cmd = catalogKey(m, key)
			after := m.journeyRecord()
			if cmd != nil || m.journey.pending != nil {
				t.Fatal("local validation scheduled an operation")
			}
			if after.problem != hint {
				t.Fatalf("local hint = %q, want %q", after.problem, hint)
			}
			if after.mutationFailure != before.mutationFailure || after.problemErr != cause || after.problemKind != "start" || !reflect.DeepEqual(after.outcomes, before.outcomes) {
				t.Fatalf("local validation lost original refusal: %+v", after)
			}
			if !reflect.DeepEqual(after.mission, context.mission) || after.state != context.state || after.receipt != context.receipt || after.question != context.question || after.answer != context.answer || after.confirm != wantConfirm || after.mode != "" || after.sourceFresh || after.answerInspected {
				t.Fatal("local validation replaced context or authorized an action")
			}
			accountText(t, m, i18n.T(hint))
			accountText(t, m, "start: credential_missing: original refusal")
			for _, kind := range []string{"refresh", "inspect", "refresh"} {
				read := &journeyRequest{kind: kind, key: req.key, mission: req.mission}
				m.journey.pending = read
				next, cmd = m.Update(journeyResult{request: read, effect: "read_only"})
				if cmd != nil {
					t.Fatal("read scheduled an operation")
				}
				m = next.(model)
				if kind == "inspect" {
					before.mutationFailure = nil
				}
				if m.journeyRecord().mutationFailure != before.mutationFailure || !reflect.DeepEqual(m.journeyRecord().outcomes, before.outcomes) {
					t.Fatal("read changed refusal acknowledgment or history")
				}
				if strings.Contains(m.View().Content, cause.Error()) != (before.mutationFailure != nil) {
					t.Fatal("visible refusal does not match inspection acknowledgment")
				}
			}
		})
	}
}
