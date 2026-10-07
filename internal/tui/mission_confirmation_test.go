package tui

import (
	"net/http"
	"strings"
	"testing"
)

func TestJourneyNewAnswerConfirmationPinsDisplayedQuestion(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	f.question(1)
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "j")
	m = catalogPaste(m, "old draft")
	m = journeyKey(t, m, "esc")
	f.question(2)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "n")
	accountText(t, m, `\"execution\": 2`)
	f.question(3)
	m = journeyFinish(t, m, m.readJourney("refresh"))
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().answer != "old draft" || m.journeyRecord().question.Target.Execution != 1 || len(f.writes) != 0 {
		t.Fatal("confirmation silently changed question/discarded text")
	}
}
func TestJourneyUnknownAnswerDoesNotBlockNewExecutionQuestion(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := newJourneyWire("codex")
	f.opened()
	f.question(1)
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/answer") && f.state.Watch.PendingQuestion.Target.Execution == 1 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("lost reply"))
			return
		}
		handler(w, req)
	})
	m := model{api: c}.selectMission(f.mission)
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "j")
	m = catalogPaste(m, "old draft")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "enter")
	m = journeyKey(t, m, "enter")
	if m.journeyRecord().uncertain != "answer" {
		t.Fatal("HTTP uncertainty not retained")
	}
	m = journeyKey(t, m, "esc")
	f.question(2)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "n")
	m = journeyKey(t, m, "enter")
	m = catalogPaste(m, "new exact answer")
	m = journeyKey(t, m, "tab")
	m = journeyKey(t, m, "enter")
	m = journeyKey(t, m, "enter")
	if len(f.answers) != 1 || f.answers[0].Execution != 2 || !strings.Contains(f.answers[0].Text, "new exact answer") {
		t.Fatal("old uncertainty blocked independent current question", m.View().Content)
	}
}
