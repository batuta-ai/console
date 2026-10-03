package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

// Pending persistence is deliberately held by the caller while real processes
// run or terminate. No catalog timing or scheduler luck determines this order.
func TestPendingAdmissionAndTerminalPersistence(t *testing.T) {
	for _, work := range []string{"interactive", "login", "init"} {
		t.Run(work, func(t *testing.T) {
			m := New(t.TempDir())
			exited := make(chan domain.Session, 4)
			m.OnExit = func(s domain.Session) { exited <- s }
			pending, err := m.StartPending(domain.Session{ID: "pending", AccountID: "a", Work: work, Argv: []string{"/bin/cat"}}, nil, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Stop("pending")
			if extra, err := m.StartPending(domain.Session{ID: "extra", AccountID: "a", Argv: []string{"/bin/cat"}}, nil, "", 1); err == nil {
				m.Stop(extra.Snapshot().ID)
				t.Fatal("pending start did not reserve capacity")
			}
			if _, err := m.Stop("pending"); err != nil {
				t.Fatal(err)
			}
			var terminal domain.Session
			select {
			case terminal = <-exited:
			case <-time.After(5 * time.Second):
				t.Fatal("no exit notification")
			}
			var written domain.Session
			got, err := pending.Persist(func(s domain.Session) error { written = s; return nil })
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range []domain.Session{written, got} {
				if s.Process != "exited" || s.EndedAt == nil || terminal.EndedAt == nil || !s.EndedAt.Equal(*terminal.EndedAt) {
					t.Fatalf("late persistence returned stale state: %+v; terminal %+v", s, terminal)
				}
			}
			next, err := m.StartPending(domain.Session{ID: "next", AccountID: "a", Argv: []string{"/bin/cat"}}, nil, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Stop(next.Snapshot().ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExitReleasesAdmissionBeforeCallbackCompletes(t *testing.T) {
	m := New(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	m.OnExit = func(s domain.Session) {
		if s.ID == "first" {
			close(entered)
			<-release
		}
	}
	first, err := m.StartPending(domain.Session{ID: "first", AccountID: "a", Argv: []string{"/usr/bin/true"}}, nil, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		m.Stop("first")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exit callback not entered")
	}
	second, err := m.StartPending(domain.Session{ID: "second", AccountID: "a", Argv: []string{"/bin/cat"}}, nil, "", 1)
	if err != nil {
		t.Fatalf("callback retained capacity: %v", err)
	}
	defer m.Stop(second.Snapshot().ID)
	if first.Snapshot().Process != "exited" {
		t.Fatal("exit not observable")
	}
	close(release)
	if _, err := m.Stop("first"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestLogOpenFailureReleasesAdmission(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	m := New(logs)
	if _, err := m.StartPending(domain.Session{ID: "failed", AccountID: "a", Argv: []string{"/bin/cat"}}, nil, "", 1); err == nil {
		t.Fatal("missing logs directory accepted")
	}
	if err := os.Mkdir(logs, 0700); err != nil {
		t.Fatal(err)
	}
	next, err := m.StartPending(domain.Session{ID: "next", AccountID: "a", Argv: []string{"/bin/cat"}}, nil, "", 1)
	if err != nil {
		t.Fatalf("spawn cleanup leaked reservation: %v", err)
	}
	if _, err := m.Stop(next.Snapshot().ID); err != nil {
		t.Fatal(err)
	}
}
