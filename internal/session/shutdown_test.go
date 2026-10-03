//go:build darwin || linux

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/unix"
)

func shutdownManager(t *testing.T, m *Manager, ctx context.Context) error {
	t.Helper()
	drainer, ok := interface{}(m).(interface{ Shutdown(context.Context) error })
	if !ok {
		t.Fatal("Manager has no bounded shutdown/admission barrier")
	}
	return drainer.Shutdown(ctx)
}

func TestShutdownDoesNotRetainCompletedDescriptorFailures(t *testing.T) {
	for _, duringShutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "draining"}[duringShutdown], func(t *testing.T) {
			m := New(t.TempDir())
			p, err := m.StartPending(domain.Session{ID: "descriptor-fault", Argv: []string{"/bin/cat"}}, nil, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = m.Stop("descriptor-fault") })
			// An actual descriptor failure is reported by cleanup, while the
			// process/group and both descriptors still finish their lifecycle.
			if err := p.live.log.Close(); err != nil {
				t.Fatal(err)
			}
			if !duringShutdown {
				if _, err := m.Stop("descriptor-fault"); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("completed cleanup must report descriptor failure: %v", err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err = m.Shutdown(ctx)
			if duringShutdown && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("drain hid current cleanup failure: %v", err)
			}
			if !duringShutdown && err != nil {
				t.Fatalf("completed descriptor failure poisoned later clean drain: %v", err)
			}
			if p.Snapshot().Process != "exited" {
				t.Fatal("failure prevented real process completion")
			}
			for _, file := range []*os.File{p.live.pty, p.live.log} {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("cleanup retained a descriptor: %v", err)
				}
			}
		})
	}
}

func TestShutdownBoundIncludesSignalSerialization(t *testing.T) {
	m := New(t.TempDir())
	p, err := m.StartPending(domain.Session{ID: "blocked-signal", Argv: []string{"/bin/cat"}}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop("blocked-signal")
	p.live.owned.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(ctx) }()
	select {
	case err := <-done:
		p.live.owned.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown bound: %v", err)
		}
	case <-time.After(2 * time.Second):
		p.live.owned.mu.Unlock()
		<-done
		t.Fatal("shutdown context could not bound serialized signal work")
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	if err := m.Shutdown(drainCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.live.pty.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("drain retained PTY descriptor: %v", err)
	}
	if _, err := p.live.log.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("drain retained log descriptor: %v", err)
	}
}

func TestShutdownIncludesAdmittedUnpublishedStart(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	ready := filepath.Join(dir, "ready")
	gate := filepath.Join(dir, "pending.log")
	for _, p := range []string{ready, gate} {
		if err := unix.Mkfifo(p, 0600); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan error, 1)
	go func() {
		_, err := m.StartPending(domain.Session{ID: "pending", AccountID: "a", Argv: []string{"/bin/sh", "-c", `printf ready >"$1"; read answer`, "leader", ready}}, nil, "", 1)
		started <- err
	}()
	// A child rendezvous proves admission and successful PTY spawn while log-open
	// still prevents publication. A FIFO reader releases that production operation.
	readyRead := make(chan error, 1)
	go func() {
		f, err := os.Open(ready)
		if err == nil {
			b := make([]byte, 5)
			_, err = f.Read(b)
			f.Close()
		}
		readyRead <- err
	}()
	select {
	case err := <-readyRead:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("spawn readiness watchdog")
	}
	var gateFile *os.File
	defer func() {
		if gateFile == nil {
			gateFile, _ = os.OpenFile(gate, os.O_RDONLY|unix.O_NONBLOCK, 0)
		}
		if gateFile != nil {
			defer gateFile.Close()
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
		}
		_, _ = m.Stop("pending")
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := shutdownManager(t, m, ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown must report bound: %v", err)
	}
	if _, err := m.StartPending(domain.Session{ID: "rejected", Argv: []string{"/bin/cat"}}, nil, "", 0); err == nil {
		m.Stop("rejected")
		t.Fatal("shutdown left admission open")
	}
	var err error
	gateFile, err = os.OpenFile(gate, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
		started <- nil
	case <-time.After(5 * time.Second):
		t.Fatal("publication watchdog")
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	if err := shutdownManager(t, m, drainCtx); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.live) != 0 || len(m.accounts) != 0 {
		t.Fatalf("drain retained live/admission: %d %v", len(m.live), m.accounts)
	}
}

func TestConcurrentStopAfterWaitAndCallbackDrain(t *testing.T) {
	m := New(t.TempDir())
	entered := make(chan domain.Session, 1)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	m.OnExit = func(s domain.Session) { entered <- s; <-release }
	p, eof := groupFixture(t, m, "tree")
	if _, err := m.Take("tree", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := m.Input("tree", "owner", []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	var terminal domain.Session
	select {
	case terminal = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exit watchdog")
	}
	requireChildEOF(t, eof)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := shutdownManager(t, m, ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unfinished callback hidden: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Stop("tree")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				errs <- err
			}
		}()
	}
	once.Do(func() { close(release) })
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := shutdownManager(t, m, ctx2); err != nil {
		t.Fatal(err)
	}
	got, err := p.Persist(func(s domain.Session) error { return errors.New("catalog unavailable") })
	if err == nil || got.EndedAt == nil || !got.EndedAt.Equal(*terminal.EndedAt) || got.Process != "exited" {
		t.Fatalf("terminal snapshot changed: %+v %v", got, err)
	}
	select {
	case s := <-entered:
		t.Fatalf("duplicate callback: %+v", s)
	default:
	}
}

func TestStopRacesNaturalExit(t *testing.T) {
	m := New(t.TempDir())
	callbacks := make(chan domain.Session, 4)
	m.OnExit = func(s domain.Session) { callbacks <- s }
	p, eof := groupFixture(t, m, "racing")
	if _, err := m.Take("racing", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, err := m.Stop("racing")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				errs <- err
			}
		}()
	}
	close(gate)
	// Stop can already have closed the transport; either input delivery or an
	// absent/closed PTY is valid. Only the owned wait can establish terminal state.
	err := m.Input("racing", "owner", []byte("exit\n"))
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, unix.EIO) {
		t.Errorf("racing input: %v", err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	requireChildEOF(t, eof)
	select {
	case terminal := <-callbacks:
		got := p.Snapshot()
		if terminal.Process != "exited" || terminal.EndedAt == nil || got.EndedAt == nil || !got.EndedAt.Equal(*terminal.EndedAt) {
			t.Fatalf("terminal state changed: %+v %+v", terminal, got)
		}
	default:
		t.Fatal("Stop returned before callback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-callbacks:
		t.Fatalf("duplicate callback: %+v", s)
	default:
	}
	if !p.live.owned.retired || p.live.cmd.ProcessState == nil {
		t.Fatal("leader not reaped after retirement")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.live) != 0 || len(m.accounts) != 0 {
		t.Fatal("runtime admission retained")
	}
}
