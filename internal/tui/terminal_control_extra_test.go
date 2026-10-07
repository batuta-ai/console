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
	c, _ := terminalFixture(t)
	base := c.HTTP.Transport
	var mutations atomic.Int32
	c.HTTP.Transport = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
		res, err := base.RoundTrip(r)
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/control") {
			mutations.Add(1)
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
	l.offer(terminalAction{operation: "tui"})
	terminalAwaitFrame(t, l, func(f terminalFrame) bool { return !f.owned && strings.Contains(f.control, "uncertain") })
	l.cancel()
	<-l.done
	if mutations.Load() != 1 {
		t.Fatalf("unknown take retried mutation %d times", mutations.Load())
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
