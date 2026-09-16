package session

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
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
	closed atomic.Bool
}

type Manager struct {
	mu     sync.Mutex
	live   map[string]*live
	logs   string
	OnExit func(domain.Session)
}

func New(logs string) *Manager {
	return &Manager{live: map[string]*live{}, logs: logs}
}

func (m *Manager) Live(id string) (*live, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.live[id]
	return l, ok
}

func (m *Manager) Start(sess domain.Session, env []string, dir string) (domain.Session, error) {
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
		return sess, err
	}
	sess.PID = cmd.Process.Pid
	sess.Process = "running"
	if sess.Owner == "" {
		sess.Owner = "none"
	}
	lf, err := os.OpenFile(logPath(m.logs, sess.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = pty.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return sess, err
	}
	l := &live{sess: sess, cmd: cmd, pty: pty, subs: map[int]Subscriber{}, log: lf}
	m.mu.Lock()
	m.live[sess.ID] = l
	m.mu.Unlock()
	go l.readLoop(m)
	go l.wait(m)
	return sess, nil
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
	_ = l.cmd.Process.Kill()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			return l.copy(), nil
		default:
			if l.closed.Load() {
				return l.copy(), nil
			}
			time.Sleep(20 * time.Millisecond)
		}
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
	l.closed.Store(true)
	if m.OnExit != nil {
		m.OnExit(snap)
	}
	_ = l.pty.Close()
	_ = l.log.Close()
	m.mu.Lock()
	delete(m.live, snap.ID)
	m.mu.Unlock()
}

func logPath(dir, id string) string {
	return dir + "/" + id + ".log"
}
