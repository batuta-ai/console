package session

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestStopWaitsForHeldFinalizationBeyondNativeBudget(t *testing.T) {
	m := New(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer func() {
		once.Do(func() { close(release) })
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()
	m.OnExit = func(domain.Session) error { close(entered); <-release; return nil }
	p, err := m.StartPending(domain.Session{ID: "held-finalization", Argv: sessionTestArgv(t, []string{"/bin/cat"})}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		session domain.Session
		err     error
	}
	stopped := make(chan result, 1)
	go func() {
		s, err := m.Stop(p.Snapshot().ID)
		stopped <- result{s, err}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("native exit/callback entry watchdog")
	}
	var got result
	early := false
	select {
	case got = <-stopped:
		early = true
		t.Errorf("Stop returned while required finalization was held: process=%s error=%v", got.session.Process, got.err)
	case <-time.After(2500 * time.Millisecond):
		t.Log("Stop remained pending beyond the original 2s wait budget")
	}
	once.Do(func() { close(release) })
	if !early {
		select {
		case got = <-stopped:
		case <-time.After(10 * time.Second):
			t.Fatal("full completion watchdog")
		}
	}
	if got.err != nil || got.session.Process != "exited" || got.session.EndedAt == nil {
		t.Errorf("Stop must report actual full completion: %+v error=%v", got.session, got.err)
	}
}

func heldStopSession(t *testing.T, argv []string, callbackErr, shutdownErr error) (*Manager, *Pending, <-chan domain.Session, func()) {
	t.Helper()
	m := New(t.TempDir())
	entered := make(chan domain.Session, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	m.OnExit = func(s domain.Session) error {
		entered <- s
		<-release
		l, ok := m.Live(s.ID)
		if !ok {
			return errors.New("callback lost live tracking")
		}
		if _, err := l.log.Stat(); err != nil {
			return err
		}
		if _, err := os.Stat(l.privateHome); err != nil {
			return err
		}
		return callbackErr
	}
	t.Cleanup(func() {
		unblock()
		err := m.Shutdown(context.Background())
		if !errors.Is(err, shutdownErr) {
			t.Errorf("final shutdown: %v, want %v", err, shutdownErr)
		}
	})
	p, err := m.StartPending(domain.Session{ID: "phases", AccountID: "account", Argv: sessionTestArgv(t, argv)}, nil, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	return m, p, entered, unblock
}

func awaitStopPhase[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("stop phase watchdog")
	}
	var zero T
	return zero
}

func assertStopNativeComplete(t *testing.T, p *Pending) {
	t.Helper()
	select {
	case <-p.live.nativeDone:
	default:
		t.Fatal("callback began before native cleanup")
	}
	select {
	case <-p.live.readDone:
	default:
		t.Fatal("native completion did not join reader")
	}
	if !ownedProcessExited(p.live.cmd, p.live.owned) {
		t.Fatal("real process has not exited")
	}
	if _, err := p.live.terminalPTY().Stat(); !errors.Is(err, stopClosedHandleError()) {
		t.Fatalf("PTY retained: %v", err)
	}
	select {
	case <-p.live.done:
		t.Fatal("full completion declared before callback release")
	default:
	}
	if _, err := p.live.log.Stat(); err != nil {
		t.Fatalf("callback log closed: %v", err)
	}
	if p.live.privateHome == "" {
		t.Fatal("fixture did not create a private home")
	}
	if _, err := os.Stat(p.live.privateHome); err != nil {
		t.Fatalf("callback home removed: %v", err)
	}
}

func TestStopContextPendingFinalization(t *testing.T) {
	for _, mode := range []string{"cancel", "cause"} {
		t.Run(mode, func(t *testing.T) {
			m, p, entered, release := heldStopSession(t, []string{"/bin/cat"}, nil, nil)
			sub, err := m.SubscribeTerminal("phases")
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			cause := error(context.Canceled)
			if mode == "cause" {
				cause = errors.New("caller stopped waiting")
			}
			type outcome struct {
				s   domain.Session
				err error
			}
			stopped := make(chan outcome, 1)
			go func() { s, err := m.StopContext(ctx, "phases"); stopped <- outcome{s, err} }()
			terminal := awaitStopPhase(t, entered)
			assertStopNativeComplete(t, p)
			drain, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			for {
				_, err := sub.Next(drain)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("subscriber not finished before callback: %v", err)
				}
			}
			m.mu.Lock()
			admitted := m.accounts["account"]
			m.mu.Unlock()
			if admitted != 0 {
				t.Fatal("callback retained admission")
			}
			cancel(cause)
			got := awaitStopPhase(t, stopped)
			var pending *FinalizationPendingError
			if !errors.As(got.err, &pending) || !errors.Is(got.err, cause) || pending.SessionID != "phases" {
				t.Fatalf("pending finalization lost cause/type: %v", got.err)
			}
			if got.s.Process != "exited" || got.s.EndedAt == nil || !got.s.EndedAt.Equal(*terminal.EndedAt) {
				t.Fatalf("untruthful pending snapshot: %+v", got.s)
			}
			if _, ok := m.Live("phases"); !ok {
				t.Fatal("pending finalizer vanished")
			}
			if err := m.Input("phases", "owner", []byte("x")); err == nil {
				t.Fatal("stop did not revoke input")
			}
			legacy := make(chan outcome, 1)
			p.live.mu.Lock()
			revoked := p.live.inputRevoked
			p.live.mu.Unlock()
			go func() { s, err := m.Stop("phases"); legacy <- outcome{s, err} }()
			awaitStopPhase(t, revoked)
			select {
			case result := <-legacy:
				t.Fatalf("legacy Stop did not wait: %+v", result)
			case <-time.After(25 * time.Millisecond):
			}
			release()
			result := awaitStopPhase(t, legacy)
			if result.err != nil || result.s.EndedAt == nil || !result.s.EndedAt.Equal(*terminal.EndedAt) {
				t.Fatalf("full completion: %+v", result)
			}
			if _, ok := m.Live("phases"); ok {
				t.Fatal("completed finalizer retained")
			}
			if _, err := p.live.log.Stat(); !errors.Is(err, stopClosedHandleError()) {
				t.Fatalf("log retained after completion: %v", err)
			}
			if _, err := os.Stat(p.live.privateHome); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("home retained after completion: %v", err)
			}
		})
	}
}

func TestStopContextAlreadyCanceledDoesNotStop(t *testing.T) {
	m := newTestManager(t)
	p, err := m.StartPending(domain.Session{ID: "untouched", Argv: sessionTestArgv(t, []string{"/bin/cat"})}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("canceled before stop")
	cancel(cause)
	got, err := m.StopContext(ctx, "untouched")
	var pending *FinalizationPendingError
	if !errors.Is(err, cause) || errors.As(err, &pending) || got.Process != "running" {
		t.Fatalf("pre-canceled request: %+v %v", got, err)
	}
	p.live.mu.Lock()
	closed := p.live.inputClosed
	p.live.mu.Unlock()
	if closed {
		t.Fatal("pre-canceled request revoked input")
	}
	if _, err := m.Take("untouched", "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := m.Input("untouched", "owner", []byte("still running\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("untouched"); err != nil {
		t.Fatal(err)
	}
}

func TestStopNativeErrorSurvivesCallerCancellation(t *testing.T) {
	m, p, entered, release := heldStopSession(t, []string{"/bin/cat"}, nil, os.ErrClosed)
	// Close the actual output descriptor: cleanup must preserve the real close error.
	if err := p.live.pty.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() { _, err := m.StopContext(ctx, "phases"); result <- err }()
	awaitStopPhase(t, entered)
	assertStopNativeComplete(t, p)
	cause := errors.New("caller canceled after native failure")
	cancel(cause)
	err := awaitStopPhase(t, result)
	var pending *FinalizationPendingError
	if !errors.Is(err, os.ErrClosed) || !errors.Is(err, cause) || !errors.As(err, &pending) {
		t.Fatalf("native failure masked by pending cancellation: %v", err)
	}
	if _, err := m.StopContext(ctx, "phases"); !errors.Is(err, os.ErrClosed) || !errors.Is(err, cause) || !errors.As(err, &pending) {
		t.Fatalf("canceled retry hid known native failure/finalization: %v", err)
	}
	if err := m.Shutdown(ctx); !errors.Is(err, os.ErrClosed) || !errors.Is(err, cause) {
		t.Fatalf("shutdown hid active native failure: %v", err)
	}
	release()
	awaitStopPhase(t, p.live.done)
	if !errors.Is(p.live.cleanupErr, os.ErrClosed) {
		t.Fatalf("full completion lost native failure: %v", p.live.cleanupErr)
	}
}

func TestStopCallbackFailureAndShutdownDrain(t *testing.T) {
	failure := errors.New("durable exit write failed")
	m, p, entered, release := heldStopSession(t, []string{"/bin/cat"}, failure, failure)
	result := make(chan error, 1)
	go func() { _, err := m.Stop("phases"); result <- err }()
	awaitStopPhase(t, entered)
	assertStopNativeComplete(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown hid pending finalizer: %v", err)
	}
	assertStopNativeComplete(t, p)
	release()
	if err := awaitStopPhase(t, result); !errors.Is(err, failure) {
		t.Fatalf("Stop lost callback failure: %v", err)
	}
	if err := m.Shutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("shutdown lost callback failure: %v", err)
	}
}

func TestStopNaturalExitRetainsCallbackFailure(t *testing.T) {
	failure := errors.New("natural exit persistence failed")
	m, p, entered, release := heldStopSession(t, []string{"/usr/bin/true"}, failure, failure)
	terminal := awaitStopPhase(t, entered)
	assertStopNativeComplete(t, p)
	release()
	awaitStopPhase(t, p.live.done)
	if !errors.Is(p.live.cleanupErr, failure) {
		t.Fatalf("completion lost callback failure: %v", p.live.cleanupErr)
	}
	if p.Snapshot().EndedAt == nil || !p.Snapshot().EndedAt.Equal(*terminal.EndedAt) {
		t.Fatal("terminal snapshot changed")
	}
	if err := m.Shutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("completed callback failure disappeared before shutdown: %v", err)
	}
}

func TestStopConcurrentNaturalFinalization(t *testing.T) {
	m, p, entered, release := heldStopSession(t, []string{"/usr/bin/true"}, nil, nil)
	terminal := awaitStopPhase(t, entered)
	assertStopNativeComplete(t, p)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			got, err := m.StopContext(ctx, "phases")
			var pending *FinalizationPendingError
			if !errors.As(err, &pending) || !errors.Is(err, context.DeadlineExceeded) || got.EndedAt == nil || !got.EndedAt.Equal(*terminal.EndedAt) {
				t.Errorf("concurrent stop lost pending completion/snapshot: %+v %v", got, err)
			}
		}()
	}
	wg.Wait()
	release()
	awaitStopPhase(t, p.live.done)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-entered:
		t.Fatalf("duplicate callback: %+v", duplicate)
	default:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.live) != 0 || len(m.accounts) != 0 {
		t.Fatal("completed session retained runtime state")
	}
}
