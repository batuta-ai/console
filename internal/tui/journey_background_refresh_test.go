package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

func backgroundRefreshModel(t *testing.T) model {
	t.Helper()
	msn := journeyMission("codex")
	m := model{screen: scrRoom, missionID: msn.ID, missions: []domain.Mission{msn}, api: client.New("http://127.0.0.1:1", "unused")}
	m.putJourney(journeyRecord{mission: msn, mode: "bind"})
	return m
}

func TestJourneyBackgroundRefreshPreservesUncertainFailure(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m := backgroundRefreshModel(t)
	r := m.journeyRecord()
	r.mode = ""
	m.putJourney(r)
	apply := func(kind, effect string, err error) {
		t.Helper()
		req := &journeyRequest{kind: kind, key: journeyID(m.roomMission()), mission: m.roomMission()}
		m.journey.pending = req
		m, _ = m.applyJourney(journeyResult{request: req, effect: effect, err: err})
	}
	apply("start", "uncertain", errors.New("transport interrupted"))
	apply("refresh", "rejected", errors.New("temporary read failure"))
	apply("refresh", "read_only", nil)
	if m.journeyRecord().effect != "uncertain" || m.journeyRecord().problem != "journey_uncertain" || m.journeyRecord().uncertain != "start" || !strings.Contains(m.View().Content, i18n.T("journey_uncertain")) || !strings.Contains(m.View().Content, "transport interrupted") || strings.Contains(m.View().Content, "temporary read failure") {
		t.Fatal("read recovery lost the uncertain action notice or displayed the read error")
	}
}

func TestJourneyKeyboardPrioritizesBackgroundRefresh(t *testing.T) {
	for _, kind := range []string{"refresh", "source", "bind", "approve", "start"} {
		t.Run(kind, func(t *testing.T) {
			m := backgroundRefreshModel(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := &journeyRequest{kind: kind, key: journeyID(m.roomMission()), ctx: ctx, cancel: cancel}
			m.journey.pending = req
			next, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
			m = next.(model)
			if kind == "refresh" {
				if m.journeyRecord().path != "x" || m.journey.pending != nil || !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("background read swallowed input: path=%q pending=%v context=%v", m.journeyRecord().path, m.journey.pending != nil, ctx.Err())
				}
				m, _ = m.applyJourney(journeyResult{request: req, effect: "read_only"})
				if m.journeyRecord().path != "x" {
					t.Fatal("canceled read changed the edited draft")
				}
			} else if m.journeyRecord().path != "" || m.journey.pending != req || ctx.Err() != nil {
				t.Fatal("foreground operation was interrupted or duplicate input was accepted")
			}
		})
	}
}

func TestJourneyBackgroundRefreshPreservesMutationFailure(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m := backgroundRefreshModel(t)
	r := m.journeyRecord()
	r.mode = ""
	m.putJourney(r)
	start := &journeyRequest{kind: "start", key: journeyID(m.roomMission()), mission: m.roomMission()}
	m.journey.pending = start
	m, _ = m.applyJourney(journeyResult{request: start, effect: "rejected", err: errors.New("credential_missing")})
	if !strings.Contains(m.View().Content, "credential_missing") {
		t.Fatal("initial mutation refusal is not visible")
	}
	apply := func(kind string) {
		t.Helper()
		req := &journeyRequest{kind: kind, key: journeyID(m.roomMission()), mission: m.roomMission()}
		m.journey.pending = req
		m, _ = m.applyJourney(journeyResult{request: req, effect: "read_only"})
	}
	apply("refresh")
	if !strings.Contains(m.View().Content, "credential_missing") || m.journeyRecord().effect != "rejected" || len(m.journeyRecord().outcomes) != 1 {
		t.Fatal("automatic read erased the mutation refusal or changed its history")
	}
	failRefresh := func() {
		t.Helper()
		req := &journeyRequest{kind: "refresh", key: journeyID(m.roomMission()), mission: m.roomMission()}
		m.journey.pending = req
		m, _ = m.applyJourney(journeyResult{request: req, effect: "rejected", err: errors.New("temporary read failure")})
		if !strings.Contains(m.View().Content, "temporary read failure") || strings.Contains(m.View().Content, "credential_missing") {
			t.Fatal("read failure displayed a different operation's error")
		}
	}
	failRefresh()
	apply("refresh")
	if !strings.Contains(m.View().Content, "credential_missing") {
		t.Fatal("read recovery acknowledged a mutation refusal without user inspection")
	}
	apply("inspect")
	if strings.Contains(m.View().Content, "credential_missing") {
		t.Fatal("explicit successful inspection did not clear the current notice")
	}
	failRefresh()
	apply("refresh")
	if strings.Contains(m.View().Content, "credential_missing") || len(m.journeyRecord().outcomes) != 1 {
		t.Fatal("automatic read resurrected an acknowledged failure or changed its history")
	}
}

func TestJourneyBackgroundRefreshPreservesEffectAndCurrentFacts(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, effect := range []string{"recorded", "rejected", "started", "uncertain", ""} {
		t.Run(effect, func(t *testing.T) {
			m := backgroundRefreshModel(t)
			r := m.journeyRecord()
			r.mode, r.effect = "", effect
			r.sourceFresh = true
			r.detail = &domain.PlanDetail{Title: "cached source"}
			cached := r.detail
			m.putJourney(r)
			fail, reads := false, 0
			watch := domain.WatchView{MissionID: r.mission.ID, WorkspaceID: r.mission.WorkspaceID, Scope: "mission", Message: "current warning"}
			m.api = accountFixture(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" || req.URL.Path != "/v0/missions/"+r.mission.ID+"/watch" {
					t.Errorf("unexpected request %s %s", req.Method, req.URL)
					http.Error(w, "unexpected", 500)
					return
				}
				reads++
				if fail {
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporary read failure"})
					return
				}
				_ = json.NewEncoder(w).Encode(watch)
			})
			expected := effect
			if expected == "" {
				expected = "read_only"
			}
			for step, failed := range []bool{false, true, false} {
				fail = failed
				if step == 2 {
					watch.Message = "updated warning"
				}
				m = journeyFinish(t, m, m.readJourney("refresh"))
				got := m.journeyRecord()
				if got.effect != expected || !strings.Contains(m.View().Content, "Last verified effect: "+expected) {
					t.Fatalf("automatic refresh caption = %q, want %q", got.effect, expected)
				}
				if got.fresh == failed || (got.problemErr != nil) != failed || got.detail != cached || !reflect.DeepEqual(got.watch, watch) || !reflect.DeepEqual(m.watch, watch) || len(got.outcomes) != 0 {
					t.Fatalf("refresh lost current facts, cached source, errors or history: %+v", got)
				}
				if failed && !strings.Contains(m.View().Content, "temporary read failure") {
					t.Fatal("automatic read failure is not visible")
				}
				if step == 0 && !got.sourceFresh {
					t.Fatal("successful watch read changed cached source freshness")
				}
				if failed && got.sourceFresh {
					t.Fatal("failed read retained source freshness")
				}
			}
			m = journeyKey(t, m, "f")
			if got := m.journeyRecord(); got.effect != "read_only" || got.problemErr != nil || !got.fresh || reads != 4 {
				t.Fatalf("explicit inspection did not acknowledge current facts: %+v reads=%d", got, reads)
			}
		})
	}
}
