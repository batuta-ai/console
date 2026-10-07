package tui

import (
	tea "charm.land/bubbletea/v2"
	"net/http"
	"testing"
	"time"
)

func TestTerminalPanelQuitJoinsAndReleases(t *testing.T) {
	m, manager := terminalAttachedFixture(t)
	m.link.offer(terminalAction{operation: "tui"})
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	result := make(chan struct{})
	go func() {
		for cmd != nil {
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); ok {
				close(result)
				return
			}
			next, cmd = next.Update(msg)
		}
	}()
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("quit did not complete")
	}
	select {
	case <-m.link.done:
	default:
		t.Fatal("quit escaped before attachment joined")
	}
	for _, id := range []string{"a-unrelated", "z-selected"} {
		s, ok := manager.Snapshot(id)
		if !ok || s.Process != "running" || s.OwnerClient != "" {
			t.Fatalf("quit retained control or killed child: %+v", s)
		}
	}
}

func TestTerminalQuitCancelsPendingAttach(t *testing.T) {
	c, _ := terminalFixture(t)
	entered := make(chan struct{})
	c.HTTP.Transport = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	m := New(c).(model)
	m.sessID = "z-selected"
	attached := make(chan tea.Msg, 1)
	go func() { attached <- m.attach()() }()
	<-entered
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	ready := cmd()
	next, cmd = next.Update(ready)
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit incomplete")
	}
	select {
	case msg := <-attached:
		if msg.(attachedMsg).err == nil {
			t.Fatal("pending dial survived")
		}
	case <-time.After(time.Second):
		t.Fatal("dial did not join")
	}
	if msg := m.attach()().(attachedMsg); msg.err == nil || msg.link != nil {
		t.Fatal("late command attached after shutdown")
	}
}
