package tui

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestJourneyOwnedCancellationBeforeAfterDispatch(t *testing.T) {
	for _, after := range []bool{false, true} {
		for _, action := range []string{"navigate", "quit", "suspend"} {
			t.Run(map[bool]string{false: "before", true: "after"}[after]+"/"+action, func(t *testing.T) {
				var calls atomic.Int32
				entered, joined := make(chan struct{}), make(chan struct{})
				c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					calls.Add(1)
					close(entered)
					defer close(joined)
					<-r.Context().Done()
				})
				m := model{api: c, clientID: "exact"}.selectMission(journeyMission("codex"))
				m = journeyKey(t, m, "b")
				m = catalogPaste(m, ".batuta/plans/exact.md")
				m = journeyKey(t, m, "tab")
				m = journeyKey(t, m, "tab")
				m = journeyKey(t, m, "enter")
				m, cmd := catalogKey(m, "enter")
				if cmd == nil {
					t.Fatal("no bind command")
				}
				req := m.journey.pending
				deadline, ok := req.ctx.Deadline()
				if !ok || time.Until(deadline) > journeyTimeout {
					t.Fatal("unbounded context")
				}
				result := make(chan tea.Msg, 1)
				if after {
					go func() { result <- cmd() }()
					select {
					case <-entered:
					case <-time.After(2 * time.Second):
						t.Fatal("no dispatch")
					}
				}
				var cancel tea.Cmd
				switch action {
				case "navigate":
					m, cancel = catalogKey(m, "esc")
				case "quit":
					m, cancel = catalogKey(m, "ctrl+c")
				case "suspend":
					m, cancel = catalogKey(m, "ctrl+z")
				}
				if cancel == nil {
					t.Fatal("focused form did not cancel/join", action)
				}
				if action == "navigate" {
					m = journeyFinish(t, m, cancel)
				} else {
					out := cancel().(accountJoined)
					if out.journey == nil {
						t.Fatal("exit omitted journey join")
					}
					m, _ = m.applyJourney(*out.journey)
					if _, ok := out.exit.(terminalExitReady); !ok {
						t.Fatal("exit not joined")
					}
				}
				if after {
					select {
					case <-joined:
					case <-time.After(2 * time.Second):
						t.Fatal("server did not observe cancellation")
					}
					m = journeyFinish(t, m, func() tea.Msg { return <-result })
				} else {
					m = journeyFinish(t, m, cmd)
				}
				want := int32(0)
				if after {
					want = 1
				}
				if calls.Load() != want || m.journey.pending != nil {
					t.Fatal("duplicate or unjoined request", calls.Load())
				}
				r := m.journey.records[req.key]
				if r.path != ".batuta/plans/exact.md" || r.fresh || (after && r.uncertain != "bind") {
					t.Fatal("draft/effect lost", r)
				}
				if action == "suspend" {
					next, replay := m.Update(terminalResumed{})
					m = next.(model)
					if replay != nil || m.journeyIO == req.lifetime {
						t.Fatal("resume replay/closed lifetime")
					}
				}
			})
		}
	}
}
func TestJourneyLateReadCannotCrossMissionOrResetTerminal(t *testing.T) {
	f := newJourneyWire("codex")
	m := model{api: accountFixture(t, f.handler(t)), sessID: "unrelated", clientID: "owner", generation: 17, link: &termLink{}}.selectMission(f.mission)
	m, cmd := catalogKey(m, "o")
	out := cmd()
	oldLink := m.link
	next := m.selectMission(journeyMission("claude"))
	cancel := next.cancelJourney()
	next = journeyFinish(t, next, cancel)
	next = journeyFinish(t, next, func() tea.Msg { return out })
	if next.journeyRecord().detail != nil || next.journeyRecord().receipt != nil || next.journeyRecord().fresh || next.sessID != "unrelated" || next.clientID != "owner" || next.generation != 17 || next.link != oldLink {
		t.Fatal("late result crossed selected mission or changed terminal")
	}
}
