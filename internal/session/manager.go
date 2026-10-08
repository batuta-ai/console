package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
)

var ErrInvalidControl = errors.New("control requires web, tui, or none and a client identity")

type Subscriber func([]byte)

type live struct {
	sess         domain.Session
	cmd          *exec.Cmd
	pty          terminalPTY
	mu           sync.Mutex
	terminalSubs map[int]*TerminalSubscription
	inputErr     error
	inputClosed  bool
	inputRevoked chan struct{}
	inputGate    chan struct{}
	pendingInput int
	next         int
	log          *os.File
	replay       []byte
	done         chan struct{}
	readDone     chan struct{}
	owned        *ownedProcess
	cleanupErr   error
	privateHome  string
}

type Manager struct {
	mu            sync.Mutex
	live          map[string]*live
	probes        map[*ownedProcess]struct{}
	logs          string
	accounts      map[string]int
	OnExit        func(domain.Session)
	closing       bool
	active        sync.WaitGroup
	shutdownOnce  sync.Once
	shutdownDone  chan struct{}
	shutdownErr   error
	unresolvedErr error
}

func New(logs string) *Manager {
	return &Manager{live: map[string]*live{}, probes: map[*ownedProcess]struct{}{}, logs: logs, accounts: map[string]int{}, shutdownDone: make(chan struct{})}
}

func (m *Manager) Live(id string) (*live, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.live[id]
	return l, ok
}

func (m *Manager) Start(sess domain.Session, env []string, dir string) (domain.Session, error) {
	pending, err := m.StartPending(sess, env, dir, 0)
	if err != nil {
		return sess, err
	}
	return pending.Snapshot(), nil
}

// Pending retains the process state through initial persistence, even if the
// process has exited and left the live map. It does not retain an admission
// after termination. Persist serializes the initial write with exit observation.
type Pending struct{ live *live }

func (p *Pending) Snapshot() domain.Session { return p.live.copy() }

func (p *Pending) Persist(write func(domain.Session) error) (domain.Session, error) {
	p.live.mu.Lock()
	defer p.live.mu.Unlock()
	err := write(p.live.sess)
	return p.live.sess, err
}

// StartPending reserves account capacity before spawning. Nonpositive capacity
// is unlimited; all account-bound starts still contribute to the same count.
func (m *Manager) StartPending(sess domain.Session, env []string, dir string, capacity int) (pending *Pending, startErr error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, fmt.Errorf("session manager is shutting down")
	}
	if sess.AccountID != "" {
		if capacity > 0 && m.accounts[sess.AccountID] >= capacity {
			m.mu.Unlock()
			return nil, fmt.Errorf("account at session capacity %d", capacity)
		}
		m.accounts[sess.AccountID]++
	}
	m.active.Add(1)
	m.mu.Unlock()
	spawned := false
	privateHome := ""
	defer func() {
		if !spawned {
			if privateHome != "" {
				startErr = errors.Join(startErr, os.RemoveAll(privateHome))
			}
			m.release(sess.AccountID)
			m.active.Done()
		}
	}()
	if accounts.ExplicitHome(env) == "" {
		privateEnv, home, err := accounts.SessionEnvironment()
		if err != nil {
			return nil, fmt.Errorf("prepare private session environment: %w", err)
		}
		privateHome = home
		env = append(append([]string{}, env...), privateEnv...)
	}

	if len(sess.Argv) == 0 {
		sess.Argv = []string{"/bin/sh"}
	}
	if sess.Cols <= 0 {
		sess.Cols = 110
	}
	if sess.Rows <= 0 {
		sess.Rows = 40
	}
	cmd := exec.Command(sess.Argv[0], sess.Argv[1:]...)
	cmd.Env = accounts.RuntimeEnvironment(os.Environ(), env)
	if dir != "" {
		cmd.Dir = dir
	}
	pty, owned, err := startManagedPTY(cmd, sess.Cols, sess.Rows)
	if err != nil {
		return nil, err
	}
	sess.PID = ownedProcessID(cmd, owned)
	sess.ProcessIdentity = ""
	sess.RecoveryReason = ""
	if observed, err := InspectProcess(sess.PID); err == nil {
		sess.ProcessIdentity = observed.Identity
	} else {
		log.Printf("observe session %s process identity: %v", sess.ID, err)
	}
	sess.Process = "running"
	if sess.Owner == "" {
		sess.Owner = "none"
	}
	lf, err := os.OpenFile(logPath(m.logs, sess.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		stopErr := owned.stop()
		waitErr := waitOwned(cmd, owned)
		closeErr := pty.Close()
		cleanupErr := errors.Join(stopErr, waitErr, closeErr)
		m.recordCleanup(cleanupErr, waitErr)
		return nil, errors.Join(err, cleanupErr)
	}
	pty, inputErr := prepareTerminalPTY(pty)
	l := &live{sess: sess, cmd: cmd, pty: pty, inputErr: inputErr, log: lf, done: make(chan struct{}), readDone: make(chan struct{}), owned: owned, privateHome: privateHome}
	m.mu.Lock()
	m.live[sess.ID] = l
	closing := m.closing
	m.mu.Unlock()
	spawned = true
	go l.readLoop(m)
	go l.wait(m)
	if closing {
		if err := l.owned.stop(); err != nil {
			m.recordCleanup(fmt.Errorf("shutdown session %s: %w", sess.ID, err), nil)
		}
	}
	return &Pending{live: l}, nil
}

func (m *Manager) Input(id, ownerClient string, data []byte) error {
	return m.InputContext(context.Background(), id, ownerClient, data)
}

func (m *Manager) Resize(id, ownerClient string, cols, rows int) error {
	l, ok := m.Live(id)
	if !ok {
		return os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sess.OwnerClient == "" || l.sess.OwnerClient != ownerClient {
		return os.ErrPermission
	}
	if cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
		return fmt.Errorf("invalid terminal dimensions")
	}
	if err := resizePTY(l.pty, cols, rows); err != nil {
		return err
	}
	l.sess.Cols, l.sess.Rows = cols, rows
	l.publishTerminal(TerminalEvent{Cols: cols, Rows: rows})
	return nil
}

func (m *Manager) Take(id, surface, client string) (domain.Session, error) {
	if client == "" || (surface != "web" && surface != "tui" && surface != "none") {
		return domain.Session{}, ErrInvalidControl
	}
	l, ok := m.Live(id)
	if !ok {
		return domain.Session{}, os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if surface == "none" {
		if l.sess.OwnerClient != client {
			return domain.Session{}, os.ErrPermission
		}
		client = ""
	}
	if l.sess.OwnerClient != client {
		l.revokeTerminalInput()
	}
	l.sess.Owner = surface
	l.sess.OwnerClient = client
	return l.sess, nil
}

func (m *Manager) Snapshot(id string) (domain.Session, bool) {
	l, ok := m.Live(id)
	if !ok {
		return domain.Session{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sess, true
}

func (m *Manager) Subscribe(id string, sub Subscriber) (func(), error) {
	subEvents, err := m.SubscribeTerminal(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := subEvents.Next(ctx)
			if err != nil {
				return
			}
			if len(e.Data) > 0 {
				sub(e.Data)
			}
		}
	}()
	return func() { cancel(); subEvents.Close(); <-done }, nil
}

func (m *Manager) Stop(id string) (domain.Session, error) {
	l, ok := m.Live(id)
	if !ok {
		return domain.Session{}, os.ErrNotExist
	}
	l.mu.Lock()
	l.inputClosed = true
	l.revokeTerminalInput()
	l.mu.Unlock()
	if err := l.owned.stop(); err != nil {
		return l.copy(), err
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-l.done:
		return l.copy(), l.cleanupErr
	case <-timer.C:
		return l.copy(), fmt.Errorf("session %s termination did not complete", id)
	}
}

func (m *Manager) release(accountID string) {
	if accountID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[accountID]--
	if m.accounts[accountID] == 0 {
		delete(m.accounts, accountID)
	}
}

func (l *live) copy() domain.Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sess
}

func (l *live) readLoop(m *Manager) {
	defer close(l.readDone)
	buf := make([]byte, 4096)
	for {
		n, err := l.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if info, err := l.log.Stat(); err == nil && info.Size() > 1<<20 {
				_ = l.log.Truncate(0)
				_, _ = l.log.Seek(0, 0)
			}
			_, _ = l.log.Write(chunk)
			l.mu.Lock()
			l.replay = append(l.replay, chunk...)
			if len(l.replay) > 64<<10 {
				l.replay = append([]byte(nil), l.replay[len(l.replay)-(64<<10):]...)
			}
			l.publishTerminal(TerminalEvent{Data: chunk})
			l.mu.Unlock()
		}
		if err != nil {
			if err != io.EOF {
				_ = err
			}
			return
		}
	}
}

func (l *live) wait(m *Manager) {
	ownershipErr := waitOwned(l.cmd, l.owned)
	cleanupErr := ownershipErr
	now := time.Now().UTC()
	l.mu.Lock()
	if ownedProcessExited(l.cmd, l.owned) {
		l.sess.Process = "exited"
		l.sess.EndedAt = &now
	} else {
		l.sess.Process = "unknown"
	}
	l.inputClosed = true
	l.revokeTerminalInput()
	snap := l.sess
	l.mu.Unlock()
	m.release(snap.AccountID)
	if m.OnExit != nil {
		m.OnExit(snap)
	}
	cleanupErr = errors.Join(cleanupErr, l.pty.Close())
	<-l.readDone
	l.mu.Lock()
	for _, sub := range l.terminalSubs {
		sub.finish()
	}
	l.mu.Unlock()
	cleanupErr = errors.Join(cleanupErr, l.log.Close())
	if l.privateHome != "" {
		homeErr := os.RemoveAll(l.privateHome)
		cleanupErr = errors.Join(cleanupErr, homeErr)
		ownershipErr = errors.Join(ownershipErr, homeErr)
	}
	l.cleanupErr = cleanupErr
	m.recordCleanup(cleanupErr, ownershipErr)
	m.mu.Lock()
	delete(m.live, snap.ID)
	m.mu.Unlock()
	close(l.done)
	m.active.Done()
}

func logPath(dir, id string) string {
	return dir + "/" + id + ".log"
}

// Shutdown permanently closes admission and waits for all admitted starts,
// readers and exit callbacks. A context bound does not cancel ongoing cleanup;
// callers can wait again, but must keep callback resources alive until it ends.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.shutdownOnce.Do(func() {
		m.mu.Lock()
		m.closing = true
		sessions := make([]*live, 0, len(m.live))
		for _, l := range m.live {
			sessions = append(sessions, l)
		}
		probes := make([]*ownedProcess, 0, len(m.probes))
		for p := range m.probes {
			probes = append(probes, p)
		}
		m.mu.Unlock()
		go func() {
			for _, l := range sessions {
				l.mu.Lock()
				l.revokeTerminalInput()
				l.mu.Unlock()
				if err := l.owned.stop(); err != nil {
					m.recordCleanup(fmt.Errorf("shutdown PID %d: %w", l.sess.PID, err), nil)
				}
			}
			for _, p := range probes {
				if err := p.stop(); err != nil {
					m.recordCleanup(err, err)
				}
			}
			m.active.Wait()
			close(m.shutdownDone)
		}()
	})
	select {
	case <-m.shutdownDone:
		m.mu.Lock()
		defer m.mu.Unlock()
		return errors.Join(m.unresolvedErr, m.shutdownErr)
	case <-ctx.Done():
		m.mu.Lock()
		defer m.mu.Unlock()
		return errors.Join(ctx.Err(), m.unresolvedErr, m.shutdownErr)
	}
}

func (m *Manager) recordCleanup(err, ownershipErr error) {
	if err == nil {
		return
	}
	log.Printf("session cleanup: %v", err)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Keep the first unresolved owned group/private-home failure, while
	// completed descriptor failures only affect their active drain.
	if ownershipErr != nil && m.unresolvedErr == nil {
		m.unresolvedErr = ownershipErr
	}
	if m.closing && m.shutdownErr == nil {
		m.shutdownErr = err
	}
}
