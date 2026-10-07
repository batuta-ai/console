package tui

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// Include dials whose results have not reached Update in owned teardown.
type terminalLifetime struct {
	mu      sync.Mutex
	closed  bool
	pending sync.WaitGroup
	next    uint64
	cancels map[uint64]context.CancelFunc
	links   map[*termLink]struct{}
}

func (l *terminalLifetime) begin() (context.Context, func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.next++
	id := l.next
	if l.cancels == nil {
		l.cancels = make(map[uint64]context.CancelFunc)
	}
	l.cancels[id] = cancel
	l.pending.Add(1)
	return ctx, func() { l.mu.Lock(); delete(l.cancels, id); l.mu.Unlock(); l.pending.Done() }, true
}
func (l *terminalLifetime) add(link *termLink) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.links == nil {
		l.links = make(map[*termLink]struct{})
	}
	l.links[link] = struct{}{}
	if l.closed {
		link.cancel()
	}
	l.pending.Add(1)
	go func() {
		defer l.pending.Done()
		<-link.done
		l.mu.Lock()
		delete(l.links, link)
		l.mu.Unlock()
	}()
}
func (l *terminalLifetime) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.closed = true
	for _, cancel := range l.cancels {
		cancel()
	}
	for link := range l.links {
		link.cancel()
	}
	l.mu.Unlock()
	l.pending.Wait()
}

type terminalExitRequest struct{ suspend bool }
type terminalExitReady struct{ suspend bool }
type terminalResumed struct{ err error }

func (m model) exitTerminal(suspend bool) (tea.Model, tea.Cmd) {
	if m.exiting {
		return m, nil
	}
	m.exiting = true
	m.termMode = false
	m.generation++
	lifetime, link := m.lifetime, m.link
	return m, func() tea.Msg {
		lifetime.close()
		if link != nil {
			link.cancel()
			<-link.done
		}
		return terminalExitReady{suspend: suspend}
	}
}
