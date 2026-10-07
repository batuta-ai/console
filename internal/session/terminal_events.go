package session

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

const TerminalProtocol = "console-terminal-v1"
const TerminalMessageLimit = 64 << 10
const terminalQueueBytes = 1 << 20
const terminalQueueEvents = 256

var ErrTerminalOutputOverflow = errors.New("terminal_output_overflow")

// TerminalEvent is ordered against replay, output and successful PTY resizes.
// Data is owned by the receiver. Geometry has nil Data; Replay is local metadata.
type TerminalEvent struct {
	Data   []byte
	Cols   int
	Rows   int
	Replay bool
}

type TerminalSubscription struct {
	mu     sync.Mutex
	events []TerminalEvent
	bytes  int
	wake   chan struct{}
	err    error
	remove func()
	once   sync.Once
	failed chan struct{}
}

func newTerminalSubscription(remove func()) *TerminalSubscription {
	return &TerminalSubscription{wake: make(chan struct{}, 1), failed: make(chan struct{}), remove: remove}
}

func (s *TerminalSubscription) enqueue(e TerminalEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	n := len(s.events)
	merge := len(e.Data) > 0 && n > 0 && len(s.events[n-1].Data) > 0 && s.events[n-1].Replay == e.Replay && len(s.events[n-1].Data)+len(e.Data) <= 4096
	if s.bytes+len(e.Data) > terminalQueueBytes || (!merge && n == terminalQueueEvents) {
		s.err = ErrTerminalOutputOverflow
		close(s.failed)
		s.events = nil
		s.bytes = 0
	} else {
		s.bytes += len(e.Data)
		if merge {
			s.events[n-1].Data = append(s.events[n-1].Data, e.Data...)
		} else {
			if e.Data != nil {
				e.Data = append([]byte{}, e.Data...)
			}
			s.events = append(s.events, e)
		}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *TerminalSubscription) Next(ctx context.Context) (TerminalEvent, error) {
	for {
		if err := ctx.Err(); err != nil {
			return TerminalEvent{}, err
		}
		s.mu.Lock()
		if len(s.events) > 0 {
			e := s.events[0]
			s.events[0] = TerminalEvent{}
			s.events = s.events[1:]
			s.bytes -= len(e.Data)
			s.mu.Unlock()
			return e, nil
		}
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return TerminalEvent{}, err
		}
		select {
		case <-ctx.Done():
			return TerminalEvent{}, ctx.Err()
		case <-s.wake:
		}
	}
}

func (s *TerminalSubscription) finish() {
	s.mu.Lock()
	if s.err == nil {
		s.err = io.EOF
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()
}

func (s *TerminalSubscription) Close() {
	s.once.Do(func() {
		if s.remove != nil {
			s.remove()
		}
		s.mu.Lock()
		s.events = nil
		s.bytes = 0
		if s.err == nil {
			s.err = io.EOF
		}
		select {
		case s.wake <- struct{}{}:
		default:
		}
		s.mu.Unlock()
	})
}

func (m *Manager) SubscribeTerminal(id string) (*TerminalSubscription, error) {
	l, ok := m.Live(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	idN := l.next
	l.next++
	sub := newTerminalSubscription(func() { l.mu.Lock(); delete(l.terminalSubs, idN); l.mu.Unlock() })
	if l.terminalSubs == nil {
		l.terminalSubs = make(map[int]*TerminalSubscription)
	}
	sub.enqueue(TerminalEvent{Cols: l.sess.Cols, Rows: l.sess.Rows})
	for offset := 0; offset < len(l.replay); offset += 4096 {
		end := min(offset+4096, len(l.replay))
		sub.enqueue(TerminalEvent{Data: l.replay[offset:end], Replay: true})
	}
	l.terminalSubs[idN] = sub
	return sub, nil
}

// Called only under the session ordering mutex; enqueue never waits on consumers.
func (l *live) publishTerminal(e TerminalEvent) {
	for _, sub := range l.terminalSubs {
		sub.enqueue(e)
	}
}

func (s *TerminalSubscription) Failed() <-chan struct{} { return s.failed }
