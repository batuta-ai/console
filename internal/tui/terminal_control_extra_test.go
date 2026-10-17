package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
)

func TestTerminalOwnershipPollFailureDisablesInput(t *testing.T) {
	for _, failure := range []string{"error", "malformed", "stalled"} {
		t.Run(failure, func(t *testing.T) {
			c, _ := terminalFixture(t)
			var enabled atomic.Bool
			base := c.HTTP.Transport
			c.HTTP.Transport = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
				if enabled.Load() && r.Method == "GET" && r.URL.Path == "/v0/sessions" {
					switch failure {
					case "error":
						return nil, fmt.Errorf("fixture ownership unavailable")
					case "malformed":
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"bad":true}`)), Request: r}, nil
					default:
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
				}
				return base.RoundTrip(r)
			})
			m := New(c).(model)
			m.sessID = "z-selected"
			msg := m.attach()().(attachedMsg)
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			l := msg.link
			t.Cleanup(func() { l.cancel(); <-l.done })
			l.offer(terminalAction{operation: "tui"})
			terminalAwaitFrame(t, l, func(f terminalFrame) bool { return f.owned })
			enabled.Store(true)
			terminalAwaitFrame(t, l, func(f terminalFrame) bool { return !f.owned && strings.Contains(f.control, "uncertain") })
			enabled.Store(false)
			l.mu.Lock()
			epoch := l.epoch
			l.mu.Unlock()
			text := "MUST NOT SEND"
			l.offer(terminalAction{paste: &text})
			l.mu.Lock()
			if l.pendingActions != 0 || l.pendingInput != 0 || l.epoch != epoch {
				t.Error("uncertain owner retained pending input")
			}
			l.mu.Unlock()
		})
	}
}

func TestTerminalExplicitReleaseStopExactSelection(t *testing.T) {
	for _, operation := range []string{"none", "stop"} {
		t.Run(operation, func(t *testing.T) {
			m, manager := terminalAttachedFixture(t)
			m.link.offer(terminalAction{operation: "tui"})
			terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
			m.link.offer(terminalAction{operation: operation})
			if operation == "none" {
				terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return !f.owned && f.owner == "" && f.control == "" })
			} else {
				select {
				case <-m.link.done:
				case <-time.After(5 * time.Second):
					t.Fatal("stop failed to join")
				}
			}
			a, ok := manager.Snapshot("a-unrelated")
			if !ok || a.Process != "running" || a.OwnerClient != "" {
				t.Fatalf("unrelated session changed: %+v", a)
			}
			s, ok := manager.Snapshot("z-selected")
			if !ok && operation != "stop" {
				t.Fatal("selected missing before stop")
			}
			if operation == "none" && (s.Process != "running" || s.OwnerClient != "") {
				t.Fatalf("release altered child: %+v", s)
			}
			if operation == "stop" && s.Process == "running" {
				t.Fatal("selected process survived explicit stop")
			}
		})
	}
}

func TestTerminalUnknownTakeNeverReleasesOrAuthorizes(t *testing.T) {
	for _, recoverTake := range []bool{false, true} {
		t.Run(fmt.Sprintf("freshTake=%t", recoverTake), func(t *testing.T) {
			c, manager := terminalFixture(t)
			base := c.HTTP.Transport
			var mutations atomic.Int32
			polls := make(chan struct{}, 1)
			c.HTTP.Transport = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" && r.URL.Path == "/v0/sessions" && mutations.Load() > 0 {
					select {
					case polls <- struct{}{}:
					default:
					}
				}
				res, err := base.RoundTrip(r)
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/control") && mutations.Add(1) == 1 {
					if res != nil {
						res.Body.Close()
					}
					return nil, fmt.Errorf("fixture lost control reply")
				}
				return res, err
			})
			m := New(c).(model)
			m.sessID = "z-selected"
			msg := m.attach()().(attachedMsg)
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			l := msg.link
			t.Cleanup(func() { l.cancel(); <-l.done })
			l.offer(terminalAction{operation: "tui"})
			lostTake := terminalAwaitFrame(t, l, func(f terminalFrame) bool {
				return !f.owned && strings.Contains(f.control, "fixture lost control reply")
			})
			awaitPoll := func() {
				t.Helper()
				select {
				case <-polls:
				case <-time.After(5 * time.Second):
					t.Fatal("ownership poll did not arrive")
				}
			}
			awaitPoll()
			for i := 0; i < 2; i++ {
				awaitPoll()
				// The next request starts after the previous reply was queued.
				// A later frame action proves that reply was processed in order.
				cols := 81 + i
				l.offer(terminalAction{event: client.TerminalEvent{Cols: cols, Rows: 24}})
				f := terminalAwaitFrame(t, l, func(f terminalFrame) bool { return f.cols == cols })
				l.mu.Lock()
				uncertain := l.ownershipUncertain
				l.mu.Unlock()
				if f.control != lostTake.control || f.owner != m.clientID || f.owned || !uncertain {
					t.Fatalf("processed poll %d lost visible uncertainty: frame=%+v ownershipUncertain=%t", i+1, f, uncertain)
				}
				text := "MUST NOT SEND"
				l.offer(terminalAction{paste: &text})
				l.mu.Lock()
				queued := l.pendingActions + l.pendingInput
				l.mu.Unlock()
				if queued != 0 || mutations.Load() != 1 {
					t.Fatalf("uncertain take queued input=%d or retried mutation=%d", queued, mutations.Load())
				}
				s, ok := manager.Snapshot(m.sessID)
				if !ok || s.OwnerClient != m.clientID || s.Owner != "tui" {
					t.Fatalf("lost reply did not mutate selected session: %+v", s)
				}
				t.Logf("processed successful poll %d: owner=%s owned=%t uncertain=%t mutations=%d", i+1, f.owner, f.owned, uncertain, mutations.Load())
			}
			if recoverTake {
				l.offer(terminalAction{operation: "tui"})
				terminalAwaitFrame(t, l, func(f terminalFrame) bool { return f.owned && f.owner == m.clientID && f.control == "" })
				l.mu.Lock()
				uncertain := l.ownershipUncertain
				l.mu.Unlock()
				if uncertain || mutations.Load() != 2 {
					t.Fatalf("fresh acknowledged take: uncertain=%t mutations=%d", uncertain, mutations.Load())
				}
				text := "FRESH TAKE INPUT"
				l.offer(terminalAction{paste: &text})
				terminalAwaitFrame(t, l, func(f terminalFrame) bool { return strings.Contains(f.text, text) })
			} else {
				l.cancel()
				<-l.done
				if mutations.Load() != 1 {
					t.Fatalf("unknown take retried mutation or released %d times", mutations.Load())
				}
			}
		})
	}
}

func TestTerminalPollBeforeTakeCannotOverrideNewEpoch(t *testing.T) {
	c, _ := terminalFixture(t)
	base := c.HTTP.Transport
	var calls atomic.Int32
	blocked, release := make(chan struct{}), make(chan struct{})
	c.HTTP.Transport = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && r.URL.Path == "/v0/sessions" && calls.Add(1) == 2 {
			close(blocked)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return base.RoundTrip(r)
	})
	m := New(c).(model)
	m.sessID = "z-selected"
	msg := m.attach()().(attachedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	l := msg.link
	t.Cleanup(func() { l.cancel(); <-l.done })
	<-blocked
	l.offer(terminalAction{operation: "tui"})
	terminalAwaitFrame(t, l, func(f terminalFrame) bool { return strings.Contains(f.control, "pending") })
	close(release)
	terminalAwaitFrame(t, l, func(f terminalFrame) bool { return f.owned && f.owner == m.clientID })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	foreign, err := c.ControlSession(ctx, m.sessID, client.ControlSessionRequest{Surface: "web", Client: "other"})
	if err != nil || foreign.OwnerClient != "other" {
		t.Fatal(err)
	}
	terminalAwaitFrame(t, l, func(f terminalFrame) bool { return !f.owned && f.owner == "other" })
	m.termMode = true
	m.Update(tea.KeyPressMsg{Text: "stale", Code: 's'})
}
