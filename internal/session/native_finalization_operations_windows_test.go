package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Candidate-only units of the real waiter core. Channel holds prove operation
// scheduling and accounting, not kernel Close, I/O interruption or Job retirement.
type finalizationOperationFixture struct {
	manager         *Manager
	control         context.Context
	stop            context.CancelFunc
	cleanup         context.Context
	cancel          context.CancelFunc
	closed          chan time.Time
	joins           [3]chan error
	sent            [3]bool
	release         [4]chan struct{}
	released        [4]bool
	calls           [4]atomic.Int32
	uncanceled      [4]atomic.Int32
	workers         sync.WaitGroup
	causes          [4]error
	operationErrors [4]error
	done            chan struct{}
	started         bool
	err             error
}

var finalizationOperationNames = [4]string{"stop", "inputClose", "outputClose", "stderrClose"}

func newFinalizationOperationFixture(held int, operationErrors bool) *finalizationOperationFixture {
	control, stop := context.WithCancel(context.Background())
	cleanup, cancel := context.WithCancel(context.Background())
	f := &finalizationOperationFixture{
		manager: &Manager{}, control: control, stop: stop, cleanup: cleanup, cancel: cancel,
		closed: make(chan time.Time, 1), done: make(chan struct{}),
	}
	for i := range f.joins {
		f.joins[i] = make(chan error, 1)
	}
	for i, name := range finalizationOperationNames {
		f.release[i] = make(chan struct{})
		if i != held {
			f.releaseOperation(i)
		}
		if operationErrors {
			f.causes[i] = errors.New(name + " independent failure")
			f.operationErrors[i] = fmt.Errorf("%s operation: %w", name, f.causes[i])
		}
	}
	return f
}

func (f *finalizationOperationFixture) operation(i int) func() error {
	return func() error {
		f.workers.Add(1)
		defer f.workers.Done()
		f.calls[i].Add(1)
		if f.cleanup.Err() == nil {
			f.uncanceled[i].Add(1)
		}
		<-f.release[i]
		return f.operationErrors[i]
	}
}

func (f *finalizationOperationFixture) start() {
	f.started = true
	go func() {
		f.err = f.manager.waitNativeFinalizationOperations(f.control, f.cancel, f.closed,
			nativeFinalizationOperations{
				stop: f.operation(0), closeInput: f.operation(1),
				closeOutput: f.operation(2), closeStderr: f.operation(3),
			}, f.joins[0], f.joins[1], f.joins[2])
		close(f.done)
	}()
	synctest.Wait()
}

func (f *finalizationOperationFixture) complete(i int, err error) {
	if !f.sent[i] {
		f.sent[i] = true
		f.joins[i] <- err
	}
}

func (f *finalizationOperationFixture) releaseOperation(i int) {
	if !f.released[i] {
		f.released[i] = true
		close(f.release[i])
	}
}

func (f *finalizationOperationFixture) finish() {
	// Every fatal path releases all barriers before any join. Buffered lifecycle
	// inputs need no producer goroutines, including after an incorrect early return.
	for i := range f.release {
		f.releaseOperation(i)
	}
	for i := range f.joins {
		f.complete(i, nil)
	}
	f.stop()
	f.cancel()
	if f.started {
		<-f.done
		// The core can no longer launch; bubble quiescence lets every already
		// launched operation enter before Wait, even if a mutant forgot pending.
		synctest.Wait()
		f.workers.Wait()
	}
}

func (f *finalizationOperationFixture) requirePending(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case <-f.done:
		t.Fatalf("TASK3_PENDING_JOIN: waiter returned while an operation was held: %v", f.err)
	default:
	}
}

func (f *finalizationOperationFixture) requireCalls(t *testing.T, want [4]int32) {
	t.Helper()
	for i, name := range finalizationOperationNames {
		if got := f.calls[i].Load(); got != want[i] {
			t.Fatalf("TASK3_ASYNC_PROGRESS: %s entries = %d, want %d while operation barriers are controlled", name, got, want[i])
		}
	}
}

func (f *finalizationOperationFixture) result(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case <-f.done:
	default:
		t.Fatal("waiter did not return after all operation and lifecycle releases")
	}
	f.workers.Wait()
}

func finalizationOperationCauseCount(err, cause error) int {
	if err == nil {
		return 0
	}
	if err == cause {
		return 1
	}
	switch err := err.(type) {
	case interface{ Unwrap() []error }:
		count := 0
		for _, child := range err.Unwrap() {
			count += finalizationOperationCauseCount(child, cause)
		}
		return count
	case interface{ Unwrap() error }:
		return finalizationOperationCauseCount(err.Unwrap(), cause)
	default:
		return 0
	}
}

func requireFinalizationOperationCauseOnce(t *testing.T, err, cause error) {
	t.Helper()
	if count := finalizationOperationCauseCount(err, cause); count != 1 || !errors.Is(err, cause) {
		t.Errorf("independent cause %v occurs %d times, want once in %v", cause, count, err)
	}
}

func TestNativeFinalizationOperationsHeldUntilJoin(t *testing.T) {
	for held, name := range finalizationOperationNames {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFinalizationOperationFixture(held, true)
				defer f.finish()
				deadline := time.Now().Add(2 * time.Second)
				f.closed <- deadline
				var callbackErr error
				if held == 0 || held == 3 {
					callbackErr = fmt.Errorf("callback: %w", errors.New("callback independent failure"))
				}
				f.complete(0, callbackErr)
				f.start()
				if got := f.calls[held].Load(); got != 1 {
					t.Fatalf("TASK3_ASYNC_PROGRESS: held %s entries = %d, want 1", name, got)
				}
				wantEarly := [4]int32{1, 1, 1, 1}
				if callbackErr == nil {
					wantEarly = [4]int32{0, 1, 1, 0}
				}
				f.requireCalls(t, wantEarly)
				if canceled := f.cleanup.Err() != nil; canceled != (callbackErr != nil) {
					t.Fatalf("cleanup canceled before deadline = %v, callback error = %v", canceled, callbackErr)
				}
				f.requirePending(t)
				f.stop()
				synctest.Wait()
				time.Sleep(time.Until(deadline))
				synctest.Wait()
				if !time.Now().Equal(deadline) {
					t.Fatalf("logical observation = %v, want deadline %v", time.Now(), deadline)
				}
				if f.cleanup.Err() != context.Canceled {
					t.Fatal("TASK3_ASYNC_PROGRESS: held operation prevented cleanup cancellation at expiry")
				}
				f.requireCalls(t, [4]int32{1, 1, 1, 1})
				for _, i := range []int{0, 3} {
					if f.uncanceled[i].Load() != 0 {
						t.Errorf("%s entered before cleanup cancellation", finalizationOperationNames[i])
					}
				}
				processErr := fmt.Errorf("process: %w", errors.New("process independent failure"))
				stderrErr := fmt.Errorf("stderr: %w", errors.New("stderr independent failure"))
				f.complete(1, processErr)
				f.complete(2, stderrErr)
				f.requirePending(t)
				for i, joined := range f.joins {
					if len(joined) != 0 {
						t.Fatalf("TASK3_ASYNC_PROGRESS: lifecycle completion %d was not consumed while %s remained held", i, name)
					}
				}
				f.manager.mu.Lock()
				progress := f.manager.unresolvedErr
				f.manager.mu.Unlock()
				for i, cause := range f.causes {
					if i != held && !errors.Is(progress, cause) {
						t.Fatalf("TASK3_ASYNC_PROGRESS: %s result was not accounted while %s remained held: %v", finalizationOperationNames[i], name, progress)
					}
				}
				time.Sleep(time.Second)
				f.requirePending(t)
				f.releaseOperation(held)
				f.result(t)
				f.requireCalls(t, [4]int32{1, 1, 1, 1})
				if !errors.Is(f.err, ErrNativeExchangeExpired) || !errors.Is(f.err, context.DeadlineExceeded) {
					t.Errorf("TASK3_EXPIRY_RETAINED: late completion lost expiry: %v", f.err)
				}
				for _, cause := range []error{ErrNativeExchangeExpired, context.DeadlineExceeded, context.Canceled, processErr, stderrErr} {
					requireFinalizationOperationCauseOnce(t, f.err, cause)
				}
				if callbackErr != nil {
					requireFinalizationOperationCauseOnce(t, f.err, callbackErr)
				}
				if !errors.Is(f.err, ErrNativeCleanupUnresolved) {
					t.Errorf("operation errors lost cleanup classification: %v", f.err)
				}
				f.manager.mu.Lock()
				closing, unresolved := f.manager.closing, f.manager.unresolvedErr
				f.manager.mu.Unlock()
				if !closing {
					t.Error("operation errors did not close admission")
				}
				for i := range f.causes {
					for _, cause := range []error{f.causes[i], f.operationErrors[i]} {
						requireFinalizationOperationCauseOnce(t, f.err, cause)
						requireFinalizationOperationCauseOnce(t, unresolved, cause)
					}
				}
			})
		})
	}
}

func TestNativeFinalizationOperationsReadyAtDeadline(t *testing.T) {
	for _, tt := range []struct {
		name    string
		offset  time.Duration
		expired bool
	}{
		{"justBefore", -time.Nanosecond, false},
		{"exactlyNow", 0, true},
		{"alreadyExpired", time.Nanosecond, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, canceled := range []bool{false, true} {
				name := "liveControl"
				if canceled {
					name = "canceledControl"
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						f := newFinalizationOperationFixture(-1, false)
						defer f.finish()
						deadline := time.Now().Add(2 * time.Second)
						f.closed <- deadline
						for i := range f.joins {
							f.complete(i, nil)
						}
						if canceled {
							f.stop()
						}
						time.Sleep(time.Until(deadline.Add(tt.offset)))
						f.start()
						f.result(t)
						if !time.Now().Equal(deadline.Add(tt.offset)) {
							t.Fatalf("ready completions advanced logical time: now=%v deadline=%v offset=%v", time.Now(), deadline, tt.offset)
						}
						for _, cause := range []error{ErrNativeExchangeExpired, context.DeadlineExceeded} {
							if errors.Is(f.err, cause) != tt.expired {
								t.Errorf("TASK3_EXPIRY_RETAINED: all-ready completion at offset %v has %v = %v, want %v: %v", tt.offset, cause, errors.Is(f.err, cause), tt.expired, f.err)
							} else if tt.expired {
								requireFinalizationOperationCauseOnce(t, f.err, cause)
							}
						}
						if errors.Is(f.err, context.Canceled) != canceled {
							t.Errorf("control cancellation mismatch: %v", f.err)
						}
						if !tt.expired && !canceled && f.err != nil {
							t.Errorf("just-before live control error = %v, want success", f.err)
						}
						if (f.cleanup.Err() != nil) != tt.expired {
							t.Errorf("cleanup cancellation = %v, want expired=%v", f.cleanup.Err(), tt.expired)
						}
						want := [4]int32{0, 1, 1, 0}
						if tt.expired {
							want = [4]int32{1, 1, 1, 1}
						}
						f.requireCalls(t, want)
						if errors.Is(f.err, ErrNativeCleanupUnresolved) || f.manager.closing || f.manager.unresolvedErr != nil {
							t.Errorf("successful operations reported cleanup failure: result=%v closing=%v unresolved=%v", f.err, f.manager.closing, f.manager.unresolvedErr)
						}
					})
				})
			}
		})
	}
}
