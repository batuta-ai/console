package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/core/publication"
)

var legacyPolicyStopErr = errors.New("legacy retired owner cleanup sentinel")

// The retired owner has no Job or process. Its error observes the real waiter's
// stop path and admission state without claiming native termination authority.
type legacyExchangePolicyFixture struct {
	manager         *Manager
	pipes           nativeExchangePipes
	owned           *ownedProcess
	stopContext     context.Context
	requestStop     context.CancelFunc
	callbackContext context.Context
	cancelCallback  context.CancelFunc
	cmd             *exec.Cmd
	stderr          *nativeOutput
	joins           [3]chan error
	sent            [3]bool
	done            chan struct{}
	finished        bool
	result          publication.CommandResult
	err             error
}

func newLegacyExchangePolicyFixture(t *testing.T) *legacyExchangePolicyFixture {
	t.Helper()
	pipes, err := newNativeExchangePipes()
	if err != nil {
		t.Fatal(err)
	}
	stopContext, requestStop := context.WithCancel(context.Background())
	callbackContext, cancelCallback := context.WithCancel(stopContext)
	f := &legacyExchangePolicyFixture{
		manager: &Manager{}, pipes: pipes,
		owned:       &ownedProcess{retired: true, err: legacyPolicyStopErr},
		stopContext: stopContext, requestStop: requestStop,
		callbackContext: callbackContext, cancelCallback: cancelCallback,
		cmd: &exec.Cmd{}, stderr: &nativeOutput{limit: 64 << 10, data: []byte("legacy stderr fixture")},
		done: make(chan struct{}),
	}
	for i := range f.joins {
		f.joins[i] = make(chan error, 1)
	}
	go func() {
		f.result, f.err = f.manager.waitNativeLegacyExchange(f.stopContext, f.cancelCallback,
			f.pipes, f.owned, f.joins[0], f.joins[1], f.joins[2],
			f.cmd, f.stderr, publication.CommandResult{ExitCode: -1})
		close(f.done)
	}()
	synctest.Wait()
	return f
}

func (f *legacyExchangePolicyFixture) release(i int) {
	if !f.sent[i] {
		f.sent[i] = true
		f.joins[i] <- nil
	}
}

func (f *legacyExchangePolicyFixture) requireConsumed(t *testing.T, i int) {
	t.Helper()
	synctest.Wait()
	if !f.sent[i] || len(f.joins[i]) != 0 {
		t.Fatalf("completion %d was not delivered and consumed", i)
	}
}

func (f *legacyExchangePolicyFixture) requirePending(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case <-f.done:
		t.Fatalf("waiter returned before required joins: %v", f.err)
	default:
	}
}

func (f *legacyExchangePolicyFixture) expiryState() (bool, bool) {
	synctest.Wait()
	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()
	return f.manager.closing, errors.Is(f.manager.unresolvedErr, legacyPolicyStopErr)
}

func (f *legacyExchangePolicyFixture) finish(t *testing.T) {
	t.Helper()
	if f.finished {
		return
	}
	f.finished = true
	for i := range f.joins {
		f.release(i)
	}
	<-f.done
	synctest.Wait()
	for i := range f.joins {
		if len(f.joins[i]) != 0 {
			t.Errorf("waiter left completion %d unconsumed", i)
		}
	}
	if f.result.ExitCode != -1 || f.result.StderrTruncated || !bytes.Equal(f.result.Stderr, []byte("legacy stderr fixture")) {
		t.Errorf("result integrity: ExitCode=%d Stderr=%q StderrTruncated=%v", f.result.ExitCode, f.result.Stderr, f.result.StderrTruncated)
	}
	if len(f.result.Stderr) > 0 {
		f.result.Stderr[0] ^= 0xff
		if !bytes.Equal(f.stderr.data, []byte("legacy stderr fixture")) {
			t.Error("result stderr aliases the capture buffer")
		}
	}
	if !errors.Is(f.callbackContext.Err(), context.Canceled) {
		t.Errorf("process completion did not cancel callback: %v", f.callbackContext.Err())
	}
	if errors.Is(f.err, context.Canceled) != (f.stopContext.Err() != nil) {
		t.Errorf("control cause mismatch: control=%v result=%v", f.stopContext.Err(), f.err)
	}
	hasProtocol := f.err != nil && strings.Contains(f.err.Error(), "native exchange did not finish after protocol closure")
	if f.stopContext.Err() != nil && (errors.Is(f.err, context.DeadlineExceeded) || hasProtocol) {
		t.Errorf("owner control unexpectedly produced a protocol deadline error: %v", f.err)
	}
	if errors.Is(f.err, ErrNativeExchangeExpired) {
		t.Errorf("legacy waiter acquired finalizing expiry error: %v", f.err)
	}
	if _, err := f.pipes.inputWrite.File.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("callback completion did not close input: %v", err)
	}
	for i, endpoint := range []*nativeExchangeFile{
		f.pipes.inputRead, f.pipes.inputWrite, f.pipes.outputRead,
		f.pipes.outputWrite, f.pipes.stderrRead, f.pipes.stderrWrite,
	} {
		if err := endpoint.Close(); err != nil {
			t.Errorf("fixture pipe %d cleanup failed: %v", i, err)
		}
	}
	f.requestStop()
	f.cancelCallback()
}

func TestNativeLegacyExchangePolicy(t *testing.T) {
	for _, tt := range []struct {
		name         string
		controlFirst bool
		second       bool
		held         int
	}{
		{"ownerControlFirst2s", true, false, -1},
		{"callbackFirstNoStop2s", false, false, -1},
		{"controlThenCallbackNoReset", true, true, -1},
		{"callbackThenControlNoReset", false, true, -1},
		{"joinCallbackAfterExpiry", true, false, 0},
		{"joinProcessAfterExpiry", true, false, 1},
		{"joinStderrAfterExpiry", true, false, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLegacyExchangePolicyFixture(t)
				defer f.finish(t)
				origin := time.Now()
				trigger := func(control bool, at time.Duration) {
					if control {
						f.requestStop()
						if !errors.Is(f.stopContext.Err(), context.Canceled) {
							t.Fatal("owner control did not cancel")
						}
					} else {
						f.release(0)
						f.requireConsumed(t, 0)
					}
					synctest.Wait()
					if elapsed := time.Since(origin); elapsed != at {
						t.Fatalf("trigger control=%v at %v, want %v", control, elapsed, at)
					}
					t.Logf("trigger control=%v at logical %s (elapsed %v)", control, time.Now().Format(time.RFC3339Nano), at)
					f.requirePending(t)
				}
				trigger(tt.controlFirst, 0)
				if tt.second {
					time.Sleep(time.Second)
					trigger(!tt.controlFirst, time.Second)
				}
				time.Sleep(time.Until(origin.Add(2*time.Second - time.Nanosecond)))
				if closing, sentinel := f.expiryState(); closing || sentinel {
					t.Fatalf("grace expired before 2s: closing=%v sentinel=%v", closing, sentinel)
				}
				f.requirePending(t)
				time.Sleep(time.Nanosecond)
				closing, sentinel := f.expiryState()
				t.Logf("expiry at elapsed %v: closing=%v sentinel=%v", time.Since(origin), closing, sentinel)
				f.requirePending(t)
				if tt.held >= 0 {
					time.Sleep(time.Nanosecond)
					for i := range f.joins {
						if i != tt.held {
							f.release(i)
							f.requireConsumed(t, i)
						}
					}
					f.requirePending(t)
					time.Sleep(time.Nanosecond)
					f.requirePending(t)
				}
				f.finish(t)
				if !closing || !sentinel {
					// Joins, cleanup, and independent checks precede this fatal on
					// every source; only signal-dependent error checks are omitted.
					t.Fatal("LEGACY_GRACE_NOT_EXPIRED_AT_2S")
				}
				for _, cause := range []error{legacyPolicyStopErr, ErrNativeCleanupUnresolved} {
					if !errors.Is(f.err, cause) {
						t.Errorf("waiter error %v lost cleanup cause %v", f.err, cause)
					}
				}
				if !tt.controlFirst && !tt.second {
					hasProtocol := f.err != nil && strings.Contains(f.err.Error(), "native exchange did not finish after protocol closure")
					if !errors.Is(f.err, context.DeadlineExceeded) || !hasProtocol {
						t.Errorf("legacy protocol deadline error missing: %v", f.err)
					}
				}
			})
		})
	}
}
