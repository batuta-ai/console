package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Keep receiving until every caller has returned, including duplicate senders
// in an incorrect implementation. The caller must join producers before finish.
func collectNativeProtocolCompletions(closed <-chan time.Time) (finish func() []time.Time) {
	stop, joined := make(chan struct{}), make(chan struct{})
	var deadlines []time.Time
	go func() {
		defer close(joined)
		for {
			select {
			case at := <-closed:
				deadlines = append(deadlines, at)
			case <-stop:
				for {
					select {
					case at := <-closed:
						deadlines = append(deadlines, at)
					default:
						return
					}
				}
			}
		}
	}()
	var once sync.Once
	return func() []time.Time {
		once.Do(func() {
			close(stop)
			<-joined
		})
		return deadlines
	}
}

func callNativeProtocolCompletionConcurrently(register func()) {
	var callers sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			for range 8 {
				register()
			}
		}()
	}
	close(start)
	callers.Wait()
}

func TestNativeProtocolCompletionRegistersExactlyOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		register, closed := newNativeProtocolCompletion()
		finish := collectNativeProtocolCompletions(closed)
		defer finish()
		callNativeProtocolCompletionConcurrently(register)
		deadlines := finish()
		if got := len(deadlines); got != 1 {
			t.Errorf("successful registrations = %d, want exactly 1", got)
		}
		if got := cap(closed); got != 1 {
			t.Errorf("registration channel capacity = %d, want 1", got)
		}
	})
}

func TestNativeProtocolCompletionRetainsRegistrationTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		register, closed := newNativeProtocolCompletion()
		finish := collectNativeProtocolCompletions(closed)
		defer finish()
		time.Sleep(time.Second)
		registeredAt := time.Now()
		register()
		synctest.Wait()
		time.Sleep(time.Second)
		callNativeProtocolCompletionConcurrently(register)
		deadlines := finish()
		if got := len(deadlines); got != 1 {
			t.Errorf("successful registrations after later repeats = %d, want exactly 1", got)
		}
		if len(deadlines) == 0 {
			t.Fatal("first registration did not notify")
		}
		if want := registeredAt.Add(2 * time.Second); !deadlines[0].Equal(want) {
			t.Errorf("registered deadline = %v, want first call + 2s = %v", deadlines[0], want)
		}
	})
}

func TestNativeProtocolCompletionDelayedConsumptionDoesNotExtendDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFinalizationPolicyFixture(t)
		defer f.finish(t)
		register, closed := newNativeProtocolCompletion()
		time.Sleep(time.Second)
		registeredAt := time.Now()
		register()
		if got := len(closed); got != 1 {
			t.Fatalf("pending registration notifications = %d, want 1 before consumption", got)
		}
		time.Sleep(time.Second)
		f.release(1, nil)
		f.release(2, nil)
		f.started = true
		go func() {
			f.err = f.manager.waitNativeFinalization(f.control, f.cancel, closed,
				f.pipes, f.owned, f.joins[0], f.joins[1], f.joins[2])
			close(f.done)
		}()
		f.requireCleanup(t, false)
		f.requirePending(t)
		if got := len(closed); got != 0 {
			t.Fatalf("pending registration notifications = %d, want actual waiter to consume it", got)
		}
		deadline := registeredAt.Add(2 * time.Second)
		time.Sleep(time.Until(deadline.Add(-time.Nanosecond)))
		f.requireCleanup(t, false)
		f.requirePending(t)
		time.Sleep(time.Nanosecond)
		f.requireCleanup(t, true)
		f.requirePending(t)
		if !time.Now().Equal(deadline) {
			t.Fatalf("expiry observation = %v, want registration + 2s = %v", time.Now(), deadline)
		}
		time.Sleep(time.Second)
		f.requirePending(t)
		f.release(0, nil)
		f.result(t, ErrNativeExchangeExpired, context.DeadlineExceeded)
		if err := f.control.Err(); err != nil {
			t.Errorf("control canceled without Stop: %v", err)
		}
		if errors.Is(f.err, context.Canceled) || errors.Is(f.err, ErrNativeCleanupUnresolved) {
			t.Errorf("unexpected control or operation error: %v", f.err)
		}
	})
}
