package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestStopFinalizationPreservesNativeFailure(t *testing.T) {
	m, p, entered, release := heldStopSession(t, []string{"/bin/cat"}, nil, os.ErrClosed)
	if err := p.live.pty.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() {
		_, err := m.StopAndFinalize(ctx, "phases", func(_ string, row domain.Session, err error) (domain.Session, error) { return row, err })
		result <- err
	}()
	awaitStopPhase(t, entered)
	assertStopNativeComplete(t, p)
	cause := errors.New("client left after native failure")
	cancel(cause)
	err := awaitStopPhase(t, result)
	var pending *FinalizationPendingError
	if !errors.Is(err, cause) || !errors.Is(err, os.ErrClosed) || !errors.As(err, &pending) {
		t.Fatalf("lost native failure: %v", err)
	}
	if err := m.Shutdown(ctx); !errors.Is(err, os.ErrClosed) || !errors.Is(err, cause) {
		t.Fatalf("active drain lost native failure: %v", err)
	}
	release()
	if err := m.Shutdown(context.Background()); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	_, err = m.StopAndFinalize(ctx, "phases", func(string, domain.Session, error) (domain.Session, error) {
		t.Error("retry duplicated finalizer")
		return domain.Session{}, nil
	})
	if !errors.Is(err, os.ErrClosed) || errors.As(err, &pending) {
		t.Fatalf("completed failure lost: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stops) != 0 || len(m.live) != 0 || len(m.stopFailures) != 1 {
		t.Fatal("completed failure retained a live job or lost its result")
	}
}

func TestStopFinalizationReleasesSuccessfulJobs(t *testing.T) {
	m := New(t.TempDir())
	t.Cleanup(func() { m.Shutdown(context.Background()) })
	for i := range 4 {
		id := fmt.Sprintf("completed-%d", i)
		if _, err := m.Start(domain.Session{ID: id, Argv: sessionTestArgv(t, []string{"/bin/cat"})}, nil, ""); err != nil {
			t.Fatal(err)
		}
		got, err := m.StopAndFinalize(context.Background(), id, func(_ string, row domain.Session, err error) (domain.Session, error) { return row, err })
		if err != nil || got.Process != "exited" {
			t.Fatalf("completion: %+v %v", got, err)
		}
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stops) != 0 || len(m.live) != 0 || len(m.stopFailures) != 0 {
		t.Fatal("successful stops accumulated completed history")
	}
}

func TestStopFinalizationAdmissionRace(t *testing.T) {
	for range 12 {
		m := New(t.TempDir())
		start := make(chan struct{})
		var callers sync.WaitGroup
		for range 8 {
			callers.Add(1)
			go func() {
				defer callers.Done()
				<-start
				_, err := m.StopAndFinalize(context.Background(), "missing", func(_ string, row domain.Session, err error) (domain.Session, error) { return row, err })
				if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrProbeClosing) {
					t.Errorf("admission race: %v", err)
				}
			}()
		}
		close(start)
		m.CloseAdmission()
		if err := m.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		callers.Wait()
		_, err := m.StopAndFinalize(context.Background(), "closed", func(string, domain.Session, error) (domain.Session, error) {
			t.Error("finalizer admitted after drain")
			return domain.Session{}, nil
		})
		if !errors.Is(err, ErrProbeClosing) {
			t.Fatal(err)
		}
		m.mu.Lock()
		if len(m.stops) != 0 || len(m.stopFailures) != 0 {
			t.Error("completed job history retained")
		}
		m.mu.Unlock()
	}
}
