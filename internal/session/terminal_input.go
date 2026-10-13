package session

import (
	"context"
	"errors"
	"os"
	"sync"
)

var ErrTerminalInputUnsupported = errors.New("terminal_input_unsupported")
var ErrTerminalInputOverflow = errors.New("terminal_input_overflow")

// TerminalInput reserves bounded pending bytes and captures the owner generation
// before waiting for another write. Revocation permanently invalidates it.
type TerminalInput struct {
	live    *live
	revoked <-chan struct{}
	owner   string
	data    []byte
	once    sync.Once
}

func (m *Manager) PrepareTerminalInput(id, owner string, data []byte) (*TerminalInput, error) {
	l, ok := m.Live(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if owner == "" || l.sess.OwnerClient != owner {
		return nil, os.ErrPermission
	}
	if l.inputClosed {
		return nil, os.ErrClosed
	}
	if l.inputErr != nil {
		return nil, l.inputErr
	}
	if len(data) > terminalQueueBytes-l.pendingInput {
		return nil, ErrTerminalInputOverflow
	}
	if l.inputRevoked == nil {
		l.inputRevoked = make(chan struct{})
	}
	if l.inputGate == nil {
		l.inputGate = make(chan struct{}, 1)
	}
	l.pendingInput += len(data)
	return &TerminalInput{live: l, revoked: l.inputRevoked, owner: owner, data: data}, nil
}

func (m *Manager) TerminalInputCapability(id string) error {
	l, ok := m.Live(id)
	if !ok {
		return os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inputErr
}

func (in *TerminalInput) Close() {
	in.once.Do(func() { in.live.mu.Lock(); in.live.pendingInput -= len(in.data); in.live.mu.Unlock() })
}

func (in *TerminalInput) Write(ctx context.Context) error {
	defer in.Close()
	l := in.live
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-in.revoked:
		return in.revocationError()
	case l.inputGate <- struct{}{}:
	}
	defer func() { <-l.inputGate }()
	for data := in.data; len(data) > 0; {
		l.mu.Lock()
		err := ctx.Err()
		if err == nil && l.inputClosed {
			err = os.ErrClosed
		}
		if err == nil {
			select {
			case <-in.revoked:
				err = os.ErrPermission
			default:
			}
		}
		if err == nil && l.sess.OwnerClient != in.owner {
			err = os.ErrPermission
		}
		n := 0
		if err == nil {
			n, err = writeTerminalChunk(l.terminalPTY(), data[:min(len(data), 4096)])
		}
		l.mu.Unlock()
		if n > 0 {
			data = data[n:]
		}
		if terminalWouldBlock(err) {
			if err = waitTerminalWritable(ctx, in.revoked, l.terminalPTY()); err != nil {
				if errors.Is(err, os.ErrPermission) {
					return in.revocationError()
				}
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (in *TerminalInput) revocationError() error {
	in.live.mu.Lock()
	defer in.live.mu.Unlock()
	if in.live.inputClosed {
		return os.ErrClosed
	}
	return os.ErrPermission
}

func (m *Manager) InputContext(ctx context.Context, id, owner string, data []byte) error {
	in, err := m.PrepareTerminalInput(id, owner, data)
	if err != nil {
		return err
	}
	return in.Write(ctx)
}

func (l *live) revokeTerminalInput() {
	if l.inputRevoked != nil {
		close(l.inputRevoked)
	}
	l.inputRevoked = make(chan struct{})
}
