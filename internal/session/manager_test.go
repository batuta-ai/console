package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestStartInputStop(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.Start(domain.Session{ID: "s1", Argv: sessionTestArgv(t, []string{"/bin/cat"}, Cols: 80, Rows: 24}), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Process != "running" || sess.PID == 0 {
		t.Fatalf("%+v", sess)
	}
	got := make(chan []byte, 8)
	unsub, err := m.Subscribe(sess.ID, func(b []byte) {
		select {
		case got <- append([]byte(nil), b...):
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	taken, err := m.Take(sess.ID, "tui", "c1")
	if err != nil || taken.Owner != "tui" {
		t.Fatalf("%+v %v", taken, err)
	}
	if err := m.Input(sess.ID, "c1", []byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no pty output")
	}
	if err := m.Input(sess.ID, "other", []byte("x")); err == nil {
		t.Fatal("non-owner must not write")
	}
	if _, err := m.Stop(sess.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReconnectDoesNotRequireNewProcess(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.Start(domain.Session{ID: "s2", Argv: sessionTestArgv(t, []string{"/bin/sleep", "8"}}), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	pid := sess.PID
	unsub, err := m.Subscribe(sess.ID, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	unsub()
	again, ok := m.Snapshot(sess.ID)
	if !ok || again.PID != pid {
		t.Fatalf("process must survive client drop: %+v", again)
	}
	_, err = m.Subscribe(sess.ID, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = m.Stop(sess.ID)
}

func TestInputRequiresOwner(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.Start(domain.Session{ID: "s3", Argv: sessionTestArgv(t, []string{"/bin/cat"}}), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.Stop(sess.ID) }()
	if err := m.Input(sess.ID, "anyone", []byte("x")); err == nil {
		t.Fatal("input without owner must fail")
	}
}

func TestStartUsesWorkspaceDir(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "here")
	if err := os.WriteFile(marker, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := make(chan []byte, 8)
	sess, err := m.Start(domain.Session{ID: "s4", Argv: sessionTestArgv(t, []string{"/bin/ls", "here"}}), nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Subscribe(sess.ID, func(b []byte) {
		select {
		case out <- append([]byte(nil), b...):
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var buf []byte
	for time.Now().Before(deadline) {
		select {
		case b := <-out:
			buf = append(buf, b...)
			if strings.Contains(string(buf), "here") {
				_, _ = m.Stop(sess.ID)
				return
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	_, _ = m.Stop(sess.ID)
	t.Fatalf("cwd output %q", buf)
}

func TestSubscribeReplays(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.Start(domain.Session{ID: "s5", Argv: sessionTestArgv(t, []string{"/bin/sh", "-c", "printf hi; read answer"}}), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan []byte, 8)
	firstUnsub, err := m.Subscribe(sess.ID, func(b []byte) {
		select {
		case ready <- append([]byte(nil), b...):
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer firstUnsub()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var output []byte
	for !strings.Contains(string(output), "hi") {
		select {
		case b := <-ready:
			output = append(output, b...)
		case <-timer.C:
			t.Fatal("process output readiness watchdog")
		}
	}
	firstUnsub()
	defer func() {
		if _, err := m.Stop(sess.ID); err != nil {
			t.Errorf("replay session cleanup: %v", err)
		}
	}()
	got := make(chan []byte, 4)
	_, err = m.Subscribe(sess.ID, func(b []byte) {
		select {
		case got <- append([]byte(nil), b...):
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		if !strings.Contains(string(b), "hi") {
			t.Fatalf("%q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected replay")
	}
}

func TestOnExitPersists(t *testing.T) {
	m := newTestManager(t)
	done := make(chan domain.Session, 1)
	m.OnExit = func(s domain.Session) { done <- s }
	if _, err := m.Start(domain.Session{ID: "s6", Argv: sessionTestArgv(t, []string{"/usr/bin/true"}}), nil, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.ID != "s6" || got.Process != "exited" || got.EndedAt == nil || got.EndedAt.IsZero() {
			t.Fatalf("%+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exit callback did not complete")
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := New(t.TempDir())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("session lifecycle cleanup: %v", err)
		}
	})
	return m
}

func TestRelinquishRequiresCurrentClientAndClearsIdentity(t *testing.T) {
	m := newTestManager(t)
	_, err := m.Start(domain.Session{ID: "release", Argv: sessionTestArgv(t, []string{"/bin/cat"}}), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop("release")
	if _, err = m.Take("release", "web", "browser-one"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Take("release", "none", "terminal-two"); !os.IsPermission(err) {
		t.Fatalf("foreign release: %v", err)
	}
	released, err := m.Take("release", "none", "browser-one")
	if err != nil || released.Owner != "none" || released.OwnerClient != "" {
		t.Fatalf("release: %+v %v", released, err)
	}
	if err := m.Input("release", "browser-one", []byte("forbidden\n")); !os.IsPermission(err) {
		t.Fatalf("released input: %v", err)
	}
	if err := m.Resize("release", "browser-one", 80, 24); !os.IsPermission(err) {
		t.Fatalf("released resize: %v", err)
	}
	if _, err = m.Take("release", "tui", "terminal-two"); err != nil {
		t.Fatal(err)
	}
	if err := m.Input("release", "terminal-two", []byte("accepted\n")); err != nil {
		t.Fatal(err)
	}
}
