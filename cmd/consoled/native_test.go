package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestDaemonDrainClosesNativeAndInteractiveAdmission(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, session.New(t.TempDir()), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := drainExecution(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session.ReserveNative(map[string]int{"selected": 1}); !errors.Is(err, session.ErrProbeClosing) {
		t.Fatal("daemon drain left native account admission open", err)
	}
	_, err = svc.Session.StartPending(domain.Session{ID: "closing", Argv: []string{"missing"}}, nil, "", 0)
	if err == nil {
		t.Fatal("daemon drain left interactive admission open")
	}
}

type drainStoreBarrier struct{ entered, release chan struct{} }

func (b drainStoreBarrier) MarshalJSON() ([]byte, error) {
	close(b.entered)
	<-b.release
	return []byte(`true`), nil
}

func TestDaemonDrainJoinsServiceStopPersistence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, session.New(t.TempDir()), nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); drainExecution(context.Background(), svc) }()
	barrierDone := make(chan error, 1)
	onExit := svc.Session.OnExit
	svc.Session.OnExit = func(row domain.Session) error {
		if err := onExit(row); err != nil {
			return err
		}
		go func() {
			barrierDone <- st.PutWorkspaceWithEvent(domain.Workspace{ID: "barrier"}, "barrier", map[string]any{"hold": drainStoreBarrier{entered, release}})
		}()
		<-entered
		return nil
	}
	live, err := svc.Session.Start(domain.Session{ID: "drain-stop", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutSession(live); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { _, err := svc.StopSessionContext(ctx, live.ID); stopped <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Store barrier watchdog")
	}
	if _, err := svc.Session.Stop(live.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-stopped:
		var pending *session.FinalizationPendingError
		if !errors.As(err, &pending) {
			t.Fatalf("caller wait: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("foreground watchdog")
	}
	short, done := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer done()
	if err := drainExecution(short, svc); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain allowed Store close: %v", err)
	}
	unblock()
	if err := drainExecution(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	if err := <-barrierDone; err != nil {
		t.Fatal(err)
	}
	events, err := st.EventsSince(0, 100)
	if err != nil || len(events) != 3 || events[2].Kind != "session.stopped" {
		t.Fatalf("drain did not join actual commit: %+v %v", events, err)
	}
}
