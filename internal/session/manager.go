package session

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

type Subscriber func([]byte)

type live struct {
	sess   domain.Session
	cmd    *exec.Cmd
	pty    *os.File
	mu     sync.Mutex
	subs   map[int]Subscriber
	next   int
	log    *os.File
	replay []byte
	done   chan struct{}
}

type Manager struct {
	mu       sync.Mutex
	live     map[string]*live
	logs     string
	accounts map[string]int
	OnExit   func(domain.Session)
}

func New(logs string) *Manager {
	return &Manager{live: map[string]*live{}, logs: logs, accounts: map[string]int{}}
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
func (m *Manager) StartPending(sess domain.Session, env []string, dir string, capacity int) (*Pending, error) {
	m.mu.Lock()
	if sess.AccountID != "" {
		if capacity > 0 && m.accounts[sess.AccountID] >= capacity {
			m.mu.Unlock()
			return nil, fmt.Errorf("account at session capacity %d", capacity)
		}
		m.accounts[sess.AccountID]++
	}
	m.mu.Unlock()
	spawned := false
	defer func() {
		if !spawned {
			m.release(sess.AccountID)
		}
	}()

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
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	if dir != "" {
		cmd.Dir = dir
	}
	pty, err := startPTY(cmd, sess.Cols, sess.Rows)
	if err != nil {
		return nil, err
	}
	sess.PID = cmd.Process.Pid
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
		closeErr := pty.Close()
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			waitErr = nil
		}
		return nil, errors.Join(err, closeErr, killErr, waitErr)
	}
	l := &live{sess: sess, cmd: cmd, pty: pty, subs: map[int]Subscriber{}, log: lf, done: make(chan struct{})}
	m.mu.Lock()
	m.live[sess.ID] = l
	m.mu.Unlock()
	spawned = true
	go l.readLoop(m)
	go l.wait(m)
	return &Pending{live: l}, nil
}

func (m *Manager) Input(id, ownerClient string, data []byte) error {
	l, ok := m.Live(id)
	if !ok {
		return os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sess.OwnerClient == "" || l.sess.OwnerClient != ownerClient {
		return os.ErrPermission
	}
	_, err := l.pty.Write(data)
	return err
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
	l.sess.Cols, l.sess.Rows = cols, rows
	return resizePTY(l.pty, cols, rows)
}

func (m *Manager) Take(id, surface, client string) (domain.Session, error) {
	l, ok := m.Live(id)
	if !ok {
		return domain.Session{}, os.ErrNotExist
	}
	l.mu.Lock()
	defer l.mu.Unlock()
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
	l, ok := m.Live(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	l.mu.Lock()
	idN := l.next
	l.next++
	l.subs[idN] = sub
	replay := append([]byte(nil), l.replay...)
	l.mu.Unlock()
	if len(replay) > 0 {
		sub(replay)
	}
	return func() {
		l.mu.Lock()
		delete(l.subs, idN)
		l.mu.Unlock()
	}, nil
}

func (m *Manager) Stop(id string) (domain.Session, error) {
	l, ok := m.Live(id)
	if !ok {
		return domain.Session{}, os.ErrNotExist
	}
	if err := l.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return l.copy(), err
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-l.done:
		return l.copy(), nil
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
			subs := make([]Subscriber, 0, len(l.subs))
			for _, s := range l.subs {
				subs = append(subs, s)
			}
			l.mu.Unlock()
			for _, s := range subs {
				s(chunk)
			}
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
	_ = l.cmd.Wait()
	now := time.Now().UTC()
	l.mu.Lock()
	l.sess.Process = "exited"
	l.sess.EndedAt = &now
	snap := l.sess
	l.mu.Unlock()
	m.release(snap.AccountID)
	if m.OnExit != nil {
		m.OnExit(snap)
	}
	_ = l.pty.Close()
	_ = l.log.Close()
	m.mu.Lock()
	delete(m.live, snap.ID)
	m.mu.Unlock()
	close(l.done)
}

func logPath(dir, id string) string {
	return dir + "/" + id + ".log"
}
