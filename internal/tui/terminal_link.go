package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

const terminalPendingLimit = 1 << 20

type terminalAction struct {
	input               bool
	inputBytes          int
	inputEpoch          uint64
	controlEpoch        uint64
	event               client.TerminalEvent
	key                 *tea.KeyPressMsg
	paste               *string
	focus               *bool
	cols, rows, history int
	operation           string
	ownership           *domain.Session
	controlResult       bool
	err                 error
}
type terminalControl struct {
	operation string
	epoch     uint64
}

type terminalWrite struct {
	data       []byte
	cols, rows int
	epoch      uint64
}

type terminalMailbox[T any] struct {
	mu    sync.Mutex
	items []T
	sizes []int
	bytes int
	wake  chan struct{}
}

func newTerminalMailbox[T any]() *terminalMailbox[T] {
	return &terminalMailbox[T]{wake: make(chan struct{}, 1)}
}
func (q *terminalMailbox[T]) push(item T, size int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.bytes+size > terminalPendingLimit || len(q.items) >= 256 {
		return false
	}
	q.items = append(q.items, item)
	q.sizes = append(q.sizes, size)
	q.bytes += size
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}
func (q *terminalMailbox[T]) pop(ctx context.Context) (T, bool) {
	var zero T
	for {
		if ctx.Err() != nil {
			return zero, false
		}
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items[0] = zero
			q.bytes -= q.sizes[0]
			q.items = q.items[1:]
			q.sizes = q.sizes[1:]
			q.mu.Unlock()
			return item, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return zero, false
		case <-q.wake:
		}
	}
}
func (q *terminalMailbox[T]) replace(match func(T) bool, item T) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := len(q.items) - 1; i >= 0; i-- {
		if match(q.items[i]) {
			q.items[i] = item
			return true
		}
	}
	return false
}

func (q *terminalMailbox[T]) take(match func(T) bool) (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var zero T
	for i, item := range q.items {
		if !match(item) {
			continue
		}
		q.bytes -= q.sizes[i]
		copy(q.items[i:], q.items[i+1:])
		q.items[len(q.items)-1] = zero
		q.items = q.items[:len(q.items)-1]
		copy(q.sizes[i:], q.sizes[i+1:])
		q.sizes = q.sizes[:len(q.sizes)-1]
		return item, true
	}
	return zero, false
}

func (q *terminalMailbox[T]) clear() {
	q.mu.Lock()
	q.items = nil
	q.sizes = nil
	q.bytes = 0
	q.mu.Unlock()
}

func (l *termLink) outputWaiting() bool {
	l.inbox.mu.Lock()
	defer l.inbox.mu.Unlock()
	return len(l.inbox.items) > 0 && len(l.inbox.items[0].event.Data) > 0 && l.inbox.items[0].event.Cols == 0
}

type termLink struct {
	api                *client.Client
	id, clientID       string
	generation         uint64
	ctx                context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	stream             *client.Terminal
	inbox              *terminalMailbox[terminalAction]
	writes             *terminalMailbox[terminalWrite]
	controls           chan terminalControl
	updates            chan struct{}
	mu                 sync.Mutex
	frame              terminalFrame
	problem            string
	owned              bool
	epoch              uint64
	writeCancel        context.CancelFunc
	releaseAttempted   bool
	controlEpoch       uint64
	pendingInput       int
	pendingActions     int
	ownershipUncertain bool
}

func newTermLink(api *client.Client, id, cid string, generation uint64, ctx context.Context, cancel context.CancelFunc, stream *client.Terminal) *termLink {
	l := &termLink{api: api, id: id, clientID: cid, generation: generation, ctx: ctx, cancel: cancel, done: make(chan struct{}), stream: stream, inbox: newTerminalMailbox[terminalAction](), writes: newTerminalMailbox[terminalWrite](), controls: make(chan terminalControl, 1), updates: make(chan struct{}, 1)}
	go l.run()
	return l
}
func (l *termLink) fail(problem string) {
	l.mu.Lock()
	if l.problem == "" {
		l.problem = problem
	}
	l.mu.Unlock()
	l.cancel()
}
func (l *termLink) offer(a terminalAction) bool {
	if l.ctx.Err() != nil {
		return false
	}
	if (a.cols != 0 || a.rows != 0) && l.inbox.replace(func(old terminalAction) bool { return old.cols != 0 || old.rows != 0 }, a) {
		return true
	}
	n := len(a.event.Data)
	if a.key != nil || a.paste != nil || a.focus != nil {
		a.input = true
		if a.key != nil {
			a.inputBytes = max(1, len(a.key.Text))
		}
		if a.paste != nil {
			a.inputBytes = len(*a.paste)
		}
		if a.focus != nil {
			a.inputBytes = 3
		}
		l.mu.Lock()
		if !l.owned || l.ctx.Err() != nil {
			l.mu.Unlock()
			return true
		}
		if l.pendingInput+l.pendingActions+a.inputBytes > terminalPendingLimit {
			l.mu.Unlock()
			l.fail("terminal input pending limit (1 MiB); nothing from this input sent")
			return false
		}
		a.inputEpoch = l.epoch
		l.pendingActions += a.inputBytes
		l.mu.Unlock()
		n += a.inputBytes
	}

	if !l.inbox.push(a, n) {
		l.fail("terminal pending queue overflow (1 MiB / 256 events)")
		return false
	}
	return true
}
func (l *termLink) publish(f terminalFrame) {
	l.mu.Lock()
	f.owned = l.owned
	l.frame = f
	l.mu.Unlock()
	select {
	case l.updates <- struct{}{}:
	default:
	}
}
func (l *termLink) next() terminalFrameMsg {
	select {
	case <-l.updates:
	case <-l.done:
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return terminalFrameMsg{sessionID: l.id, clientID: l.clientID, generation: l.generation, frame: l.frame}
}
func (l *termLink) invalidate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.owned = false
	l.epoch++
	l.pendingInput = 0
	l.pendingActions = 0
	if l.writeCancel != nil {
		l.writeCancel()
		l.writeCancel = nil
	}
	l.writes.clear()
}
func (l *termLink) outgoing(data []byte, cols, rows int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.owned || l.ctx.Err() != nil {
		return true
	}
	if l.pendingInput+l.pendingActions+len(data) > terminalPendingLimit {
		return false
	}
	w := terminalWrite{data: append([]byte(nil), data...), cols: cols, rows: rows, epoch: l.epoch}
	if data == nil && l.writes.replace(func(old terminalWrite) bool { return old.cols != 0 }, w) {
		return true
	}
	if !l.writes.push(w, len(data)) {
		return false
	}
	l.pendingInput += len(data)
	return true
}
func (l *termLink) writeLoop() {
	for {
		w, ok := l.writes.pop(l.ctx)
		if !ok {
			return
		}
		l.mu.Lock()
		if !l.owned || l.epoch != w.epoch || l.ctx.Err() != nil {
			l.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(l.ctx)
		l.writeCancel = cancel
		l.mu.Unlock()
		var err error
		if w.data != nil {
			err = l.stream.Input(ctx, w.data)
		} else {
			err = l.stream.Resize(ctx, w.cols, w.rows)
		}
		cancel()
		l.mu.Lock()
		current := w.epoch == l.epoch
		if current {
			l.pendingInput -= len(w.data)
		}
		l.mu.Unlock()
		if err != nil && current {
			l.fail("terminal write uncertain; input was not acknowledged: " + err.Error())
			return
		}
	}
}
func (l *termLink) readLoop() {
	for {
		event, err := l.stream.Read(l.ctx)
		if err != nil {
			l.offer(terminalAction{err: err})
			return
		}
		if !l.offer(terminalAction{event: event}) {
			return
		}
	}
}
func (l *termLink) control(ctx context.Context, operation string) (domain.Session, error) {
	if operation == "stop" {
		return l.api.StopSession(ctx, l.id)
	}
	return l.api.ControlSession(ctx, l.id, client.ControlSessionRequest{Surface: operation, Client: l.clientID})
}
func (l *termLink) session(ctx context.Context) (domain.Session, error) {
	sessions, err := l.api.ListSessions(ctx)
	if err != nil {
		return domain.Session{}, err
	}
	for _, s := range sessions {
		if s.ID == l.id {
			return s, nil
		}
	}
	return domain.Session{}, fmt.Errorf("selected session missing")
}
func (l *termLink) controlLoop() {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	poll := func() {
		l.mu.Lock()
		epoch := l.controlEpoch
		l.mu.Unlock()
		ctx, cancel := context.WithTimeout(l.ctx, 2*time.Second)
		s, err := l.session(ctx)
		cancel()
		l.offer(terminalAction{ownership: &s, err: err, controlEpoch: epoch})
	}
	poll()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-timer.C:
			poll()
		case request := <-l.controls:
			op := request.operation
			l.mu.Lock()
			current := request.epoch == l.controlEpoch && l.ctx.Err() == nil
			l.mu.Unlock()
			if !current {
				continue
			}

			if op == "none" {
				l.mu.Lock()
				l.releaseAttempted = true
				l.mu.Unlock()
			}
			ctx, cancel := context.WithTimeout(l.ctx, 2*time.Second)
			s, err := l.control(ctx, op)
			cancel()
			if err != nil {
				l.mu.Lock()
				l.ownershipUncertain = true
				l.mu.Unlock()
			}
			if op == "stop" && err == nil {
				l.fail("selected session stopped")
				return
			}
			if !l.offer(terminalAction{ownership: &s, controlResult: true, operation: op, err: err, controlEpoch: request.epoch}) {
				return
			}
		}
	}
}
func (l *termLink) run() {
	var producers sync.WaitGroup
	for _, f := range []func(){l.readLoop, l.writeLoop, l.controlLoop} {
		producers.Add(1)
		go func(f func()) { defer producers.Done(); f() }(f)
	}
	s := &terminalScreen{}
	defer func() {
		l.cancel()
		l.invalidate()
		_ = l.stream.Close()
		producers.Wait()
		l.mu.Lock()
		problem := l.problem
		attempted := l.releaseAttempted
		uncertain := l.ownershipUncertain
		l.mu.Unlock()
		if uncertain {
			problem += "; ownership uncertain after control request"
		}
		if !attempted && !uncertain {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			current, err := l.session(ctx)
			if err == nil && current.OwnerClient == l.clientID {
				_, err = l.control(ctx, "none")
			}
			cancel()
			if err != nil {
				problem += "; ownership uncertain: " + err.Error()
			}
		}
		l.inbox.clear()
		l.writes.clear()
		if s.parser != nil {
			s.parser.SetResponseCallback(nil)
		}
		f := s.frame
		f.problem = problem
		f.disconnected = true
		if f.problem == "" {
			f.problem = "disconnected; explicit reconnect required"
		}
		s.parser = nil
		s.screen = nil
		l.publish(f)
		close(l.done)
	}()
	cols, rows := 0, 0
	pending := ""
	unpresented := 0
	for {
		if l.ctx.Err() != nil {
			return
		}
		var a terminalAction
		var ok bool
		if pending != "" {
			a, ok = l.inbox.take(func(a terminalAction) bool {
				return a.event.Data == nil && a.event.Cols == 0 && (a.err == nil || a.ownership != nil)
			})
			if !ok {
				n := s.feedTurn(l.ctx, cols, rows, pending)
				pending = pending[n:]
				if s.failed {
					l.fail(s.frame.problem)
					return
				}
				if pending == "" {
					// Coalesce already queued output, never across geometry or
					// another action. An empty queue presents the final glyph
					// immediately; continuous output presents at least every64KiB.
					if unpresented >= 64<<10 || !l.outputWaiting() {
						l.publish(s.present())
						unpresented = 0
					}
				}
				continue
			}
		} else {
			a, ok = l.inbox.pop(l.ctx)
		}
		if !ok {
			return
		}
		if a.input {
			l.mu.Lock()
			current := a.inputEpoch == l.epoch && l.owned
			if current {
				l.pendingActions -= a.inputBytes
			}
			l.mu.Unlock()
			if !current {
				continue
			}
		}
		if a.ownership != nil {
			l.mu.Lock()
			current := a.controlEpoch == l.controlEpoch
			l.mu.Unlock()
			if !current {
				continue
			}
			if a.err != nil {
				l.invalidate()
				s.frame.control = "ownership uncertain: " + a.err.Error()
			} else if a.ownership.ID != l.id {
				l.invalidate()
				s.frame.control = "ownership response session mismatch"
			} else {
				ours := a.ownership.OwnerClient == l.clientID && a.ownership.Owner == "tui"
				if !ours {
					l.invalidate()
				}
				if a.controlResult && a.operation == "tui" && ours {
					l.mu.Lock()
					l.owned = true
					l.epoch++
					l.releaseAttempted = false
					l.ownershipUncertain = false
					l.mu.Unlock()
				}
				s.frame.control = ""
				s.frame.owner = a.ownership.OwnerClient
			}
			l.publish(s.frame)
			continue
		}
		if a.err != nil {
			l.fail("disconnected; explicit reconnect required: " + a.err.Error())
			return
		}
		if a.operation != "" {
			l.invalidate()
			l.mu.Lock()
			l.controlEpoch++
			epoch := l.controlEpoch
			l.mu.Unlock()
			select {
			case l.controls <- terminalControl{operation: a.operation, epoch: epoch}:
				s.frame.control = "control request pending; input disabled"
			default:
				s.frame.control = "control request already pending; ownership uncertain"
			}
			l.publish(s.frame)
			continue
		}

		if a.event.Cols != 0 {
			cols, rows = a.event.Cols, a.event.Rows
			s.feedContext(l.ctx, cols, rows, "")
		}
		if a.event.Data != nil {
			if unpresented > 0 && unpresented+len(a.event.Data) > 64<<10 {
				l.publish(s.present())
				unpresented = 0
			}
			pending = string(a.event.Data)
			unpresented += len(pending)
			continue
		}
		if s.parser != nil {
			s.parser.SetResponseCallback(func(data []byte) {
				if !l.outgoing(data, 0, 0) {
					l.fail("terminal reply queue overflow")
				}
			})
		}
		if s.failed {
			l.fail(s.frame.problem)
			return
		}
		if a.input && s.unsupported {
			s.frame.problem = "unsupported child geometry; input unavailable until supported resize"
			l.publish(s.frame)
			continue
		}
		var data []byte
		var err error
		switch {
		case a.key != nil:
			data, err = encodeTerminalKey(*a.key, s.observer)
		case a.paste != nil:
			data, err = encodeTerminalPaste(*a.paste, s.observer.paste)
		case a.focus != nil && s.observer.focus:
			if *a.focus {
				data = []byte("\x1b[I")
			} else {
				data = []byte("\x1b[O")
			}
		case a.cols != 0 || a.rows != 0:
			if a.rows < 2 || a.cols < 1 {
				s.frame.problem = "terminal viewport too small (requires two rows)"
			} else if !l.outgoing(nil, min(a.cols, 500), min(a.rows, 200)) {
				l.fail("terminal resize queue overflow")
			}
		case a.history != 0:
			if s.screen != nil && !s.observer.alternate && !s.observer.held {
				if a.history > 0 {
					s.screen.ScrollViewUp(a.history)
				} else {
					s.screen.ScrollViewDown(-a.history)
				}
				s.frame = s.snapshot()
			}
		}
		if err != nil {
			s.frame.problem = err.Error()
		}
		if len(data) > 0 {
			if s.screen != nil && !s.observer.held {
				s.screen.ResetView()
				s.frame = s.snapshot()
			}
			if !l.outgoing(data, 0, 0) {
				l.fail("terminal input queue overflow")
				return
			}
		}
		l.publish(s.frame)
	}
}
