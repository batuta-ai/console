package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestStartInputStop(t *testing.T) {
	m := New(t.TempDir())
	sess, err := m.Start(domain.Session{ID: "s1", Argv: []string{"/bin/cat"}, Cols: 80, Rows: 24}, nil, "")
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
	m := New(t.TempDir())
	sess, err := m.Start(domain.Session{ID: "s2", Argv: []string{"/bin/sleep", "8"}}, nil, "")
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
	m := New(t.TempDir())
	sess, err := m.Start(domain.Session{ID: "s3", Argv: []string{"/bin/cat"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.Stop(sess.ID) }()
	if err := m.Input(sess.ID, "anyone", []byte("x")); err == nil {
		t.Fatal("input without owner must fail")
	}
}

func TestStartUsesWorkspaceDir(t *testing.T) {
	m := New(t.TempDir())
	dir := t.TempDir()
	marker := filepath.Join(dir, "here")
	if err := os.WriteFile(marker, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := make(chan []byte, 8)
	sess, err := m.Start(domain.Session{ID: "s4", Argv: []string{"/bin/ls", "here"}}, nil, dir)
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
	m := New(t.TempDir())
	sess, err := m.Start(domain.Session{ID: "s5", Argv: []string{"/bin/sh", "-c", "printf hi; sleep 2"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.Stop(sess.ID) }()
	time.Sleep(200 * time.Millisecond)
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
	m := New(t.TempDir())
	var got domain.Session
	m.OnExit = func(s domain.Session) { got = s }
	if _, err := m.Start(domain.Session{ID: "s6", Argv: []string{"/usr/bin/true"}}, nil, ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got.Process == "exited" && got.EndedAt != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%+v", got)
}
