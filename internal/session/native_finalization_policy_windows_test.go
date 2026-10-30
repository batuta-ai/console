package session

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"testing/synctest"
	"time"
)

// These unit inputs exercise the production waiter, not ProtocolDone validation
// or registration. The retired owner has no Job, process, or force authority.
type finalizationPolicyFixture struct {
	manager *Manager
	pipes   nativeExchangePipes
	owned   *ownedProcess
	control context.Context
	stop    context.CancelFunc
	cleanup context.Context
	cancel  context.CancelFunc
	closed  chan time.Time
	joins   [3]chan error
	sent    [3]bool
	done    chan struct{}
	started bool
	err     error
}

func newFinalizationPolicyFixture(t *testing.T) *finalizationPolicyFixture {
	t.Helper()
	pipes, err := newNativeExchangePipes()
	if err != nil {
		t.Fatal(err)
	}
	control, stop := context.WithCancel(context.Background())
	cleanup, cancel := context.WithCancel(context.Background())
	f := &finalizationPolicyFixture{
		manager: &Manager{}, pipes: pipes, owned: &ownedProcess{retired: true},
		control: control, stop: stop, cleanup: cleanup, cancel: cancel,
		closed: make(chan time.Time, 1), done: make(chan struct{}),
	}
	for i := range f.joins {
		f.joins[i] = make(chan error, 1)
	}
	t.Logf("UNIT waiter: runtime=%s GOOS=%s GOARCH=%s GOMAXPROCS=%d CPUs=%d; logical time only",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), runtime.NumCPU())
	return f
}

func (f *finalizationPolicyFixture) start() {
	f.started = true
	go func() {
		f.err = f.manager.waitNativeFinalization(f.control, f.cancel, f.closed,
			f.pipes, f.owned, f.joins[0], f.joins[1], f.joins[2])
		close(f.done)
	}()
	synctest.Wait()
}

func (f *finalizationPolicyFixture) release(i int, err error) {
	if !f.sent[i] {
		f.sent[i] = true
		f.joins[i] <- err
	}
}

func (f *finalizationPolicyFixture) finish(t *testing.T) {
	t.Helper()
	for i := range f.joins {
		f.release(i, nil)
	}
	if f.started {
		<-f.done
		synctest.Wait()
	}
	f.stop()
	f.cancel()
	for _, endpoint := range []*nativeExchangeFile{
		f.pipes.inputRead, f.pipes.inputWrite, f.pipes.outputRead,
		f.pipes.outputWrite, f.pipes.stderrRead, f.pipes.stderrWrite,
	} {
		if err := endpoint.Close(); err != nil && !errors.Is(f.err, err) {
			t.Errorf("unreported fixture cleanup error: %v", err)
		}
	}
}

func (f *finalizationPolicyFixture) requirePending(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case <-f.done:
		t.Fatalf("waiter returned before required joins: %v", f.err)
	default:
	}
}

func (f *finalizationPolicyFixture) requireCleanup(t *testing.T, canceled bool) {
	t.Helper()
	synctest.Wait()
	if got := f.cleanup.Err(); (got != nil) != canceled {
		t.Fatalf("cleanup cancellation = %v, want canceled=%v at logical %v", got, canceled, time.Now())
	}
}

func (f *finalizationPolicyFixture) result(t *testing.T, causes ...error) {
	t.Helper()
	<-f.done
	synctest.Wait()
	for _, cause := range causes {
		if !errors.Is(f.err, cause) {
			t.Errorf("waiter error %v lost independent cause %v", f.err, cause)
		}
	}
}

func TestNativeFinalizationPolicyDeadlines(t *testing.T) {
	type event struct {
		at       time.Duration
		control  bool
		done     bool
		progress bool
		callback bool
	}
	for _, tt := range []struct {
		name        string
		events      []event
		consumeAt   time.Duration
		wantExpired time.Duration
	}{
		{"ownerControlOnly5s", []event{{control: true}}, 0, 5 * time.Second},
		{"controlAfterStartFromObservation", []event{{}, {at: time.Second, control: true}}, 0, 6 * time.Second},
		{"suppliedDoneOnly2sNoStop", []event{{done: true}}, 0, 2 * time.Second},
		{"controlThenEarlierDone", []event{{control: true}, {at: time.Second, done: true}}, 0, 3 * time.Second},
		{"controlThenLaterDone", []event{{control: true}, {at: 4 * time.Second, done: true}}, 0, 5 * time.Second},
		{"doneThenLaterControl", []event{{done: true}, {at: time.Second, control: true}}, 0, 2 * time.Second},
		{"delayedTimestampConsumption", []event{{done: true}}, time.Second, 2 * time.Second},
		{"processAndStderrSuccessDoNotReset", []event{{control: true}, {at: 3 * time.Second, progress: true}}, 0, 5 * time.Second},
		{"callbackSuccessDoesNotReset", []event{{control: true}, {at: 3 * time.Second, callback: true}}, 0, 5 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFinalizationPolicyFixture(t)
				defer f.finish(t)
				origin := time.Now()
				for i, e := range tt.events {
					time.Sleep(time.Until(origin.Add(e.at)))
					if e.control {
						f.stop()
					}
					if e.done {
						// A supplied validation deadline is not proof of registration.
						f.closed <- time.Now().Add(2 * time.Second)
					}
					if e.progress {
						f.release(1, nil)
						f.release(2, nil)
					}
					if e.callback {
						f.release(0, nil)
					}
					if i == 0 {
						time.Sleep(tt.consumeAt)
						f.start()
					}
					f.requireCleanup(t, false)
					f.requirePending(t)
				}
				time.Sleep(time.Until(origin.Add(tt.wantExpired - time.Nanosecond)))
				f.requireCleanup(t, false)
				f.requirePending(t)
				time.Sleep(time.Nanosecond)
				f.requireCleanup(t, true)
				f.requirePending(t)
				for i := range f.joins {
					f.release(i, nil)
				}
				f.result(t, ErrNativeExchangeExpired, context.DeadlineExceeded)
				if errors.Is(f.err, ErrNativeCleanupUnresolved) {
					t.Errorf("unexpected operation error: %v", f.err)
				}
				if errors.Is(f.err, context.Canceled) != (f.control.Err() != nil) {
					t.Errorf("control cause mismatch: control=%v result=%v", f.control.Err(), f.err)
				}
			})
		})
	}
}

func TestNativeFinalizationPolicyCompletesBeforeDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFinalizationPolicyFixture(t)
		defer f.finish(t)
		deadline := time.Now().Add(2 * time.Second)
		f.closed <- deadline
		f.start()
		f.requirePending(t)
		time.Sleep(time.Second)
		for i := range f.joins {
			f.release(i, nil)
		}
		synctest.Wait()
		select {
		case <-f.done:
		default:
			t.Fatal("waiter did not return after all required joins")
		}
		if !time.Now().Before(deadline) {
			t.Fatal("completion assertion reached the supplied deadline")
		}
		if f.err != nil {
			t.Errorf("waiter error = %v, want nil", f.err)
		}
		if err := f.cleanup.Err(); err != nil {
			t.Errorf("cleanup canceled before deadline: %v", err)
		}
	})
}

func TestNativeFinalizationPolicyRequiresEachLifecycleJoin(t *testing.T) {
	for held, name := range []string{"callbackAfterProcess", "processAfterCallback", "stderrAfterProcessAndCallback"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFinalizationPolicyFixture(t)
				defer f.finish(t)
				for i := range f.joins {
					if i != held {
						f.release(i, nil)
					}
				}
				f.closed <- time.Now().Add(2 * time.Second)
				f.start()
				f.requirePending(t)
				f.requireCleanup(t, false)
				time.Sleep(2 * time.Second)
				f.requireCleanup(t, true)
				f.requirePending(t)
				time.Sleep(time.Second)
				f.requirePending(t)
				cause := errors.New(name + " independent failure")
				f.release(held, cause)
				f.result(t, ErrNativeExchangeExpired, context.DeadlineExceeded, cause)
				if errors.Is(f.err, ErrNativeCleanupUnresolved) {
					t.Errorf("unexpected operation error: %v", f.err)
				}
			})
		})
	}
}

func TestNativeFinalizationPolicyJoinsOperationErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFinalizationPolicyFixture(t)
		defer f.finish(t)
		stopErr := errors.New("retired owner cleanup failure")
		f.owned.err = stopErr
		endpoints := []*nativeExchangeFile{f.pipes.inputWrite, f.pipes.outputRead, f.pipes.stderrRead}
		for _, endpoint := range endpoints {
			// Closing the raw file makes the real once-only Close return its own
			// PathError. There is no pending OS I/O or substituted Close function.
			if err := endpoint.File.Close(); err != nil {
				t.Fatal(err)
			}
		}
		f.stop()
		f.closed <- time.Now().Add(2 * time.Second)
		f.start()
		time.Sleep(2 * time.Second)
		f.requireCleanup(t, true)
		f.requirePending(t)
		causes := []error{ErrNativeExchangeExpired, context.DeadlineExceeded, context.Canceled, ErrNativeCleanupUnresolved, stopErr}
		for i, name := range []string{"callback", "process", "stderr"} {
			cause := errors.New(name + " independent failure")
			causes = append(causes, cause)
			f.release(i, cause)
			if i < len(f.joins)-1 {
				f.requirePending(t)
			}
		}
		f.result(t, causes...)
		f.manager.mu.Lock()
		defer f.manager.mu.Unlock()
		for i, endpoint := range endpoints {
			if !errors.Is(endpoint.err, os.ErrClosed) || !errors.Is(f.err, endpoint.err) {
				t.Errorf("operation %d close cause missing: close=%v result=%v", i, endpoint.err, f.err)
			}
			if !errors.Is(f.manager.unresolvedErr, endpoint.err) {
				t.Errorf("operation %d close cause not retained by Manager: close=%v unresolved=%v", i, endpoint.err, f.manager.unresolvedErr)
			}
		}
		if !f.manager.closing || !errors.Is(f.manager.unresolvedErr, stopErr) {
			t.Errorf("cleanup failure not retained by Manager: closing=%v unresolved=%v", f.manager.closing, f.manager.unresolvedErr)
		}
	})
}
