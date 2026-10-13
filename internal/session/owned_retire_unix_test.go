//go:build darwin || linux

package session

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func exitedOwnedFixture(t *testing.T) (*exec.Cmd, *ownedProcess) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	if err := observeOwnedExit(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	return cmd, newOwnedProcess(cmd)
}

func TestOwnedRetireRetainsGroupObservationFailure(t *testing.T) {
	_, owned := exitedOwnedFixture(t)
	failure := errors.New("group observation failed")
	if err := owned.retireWithGroupWait(nil, func(int) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("initial retirement: %v", err)
	}
	if err := owned.retire(nil); !errors.Is(err, failure) {
		t.Fatalf("repeated retirement lost cleanup failure: %v", err)
	}
}

func TestOwnedRetireDoesNotBlockStopDuringObservation(t *testing.T) {
	_, owned := exitedOwnedFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- owned.retireWithGroupWait(nil, func(int) error { close(entered); <-release; return nil })
	}()
	<-entered
	stop := make(chan error, 1)
	go func() { stop <- owned.stop() }()
	select {
	case err := <-stop:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("Stop blocked on group observation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !owned.retired {
		t.Fatal("completed observation did not retire authority")
	}
}

func TestOwnedRetireClearsOnlyVerifiedPendingError(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		signal, group         error
		wantSignal, wantGroup bool
	}{
		{name: "verified pending", signal: errGroupExiting},
		{name: "pending timeout", signal: errGroupExiting, group: errGroupExiting, wantSignal: true, wantGroup: true},
		{name: "unknown signal error", signal: syscall.EPERM, wantSignal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, owned := exitedOwnedFixture(t)
			err := owned.retireWithGroupWait(nil, func(int) error {
				owned.mu.Lock()
				owned.err = tc.signal
				owned.mu.Unlock()
				return tc.group
			})
			if errors.Is(err, tc.signal) != tc.wantSignal {
				t.Fatalf("signal outcome: %v", err)
			}
			if tc.wantGroup && !errors.Is(err, tc.group) {
				t.Fatalf("group outcome: %v", err)
			}
			if !owned.retired {
				t.Fatal("observation did not retire authority")
			}
			if again := owned.retire(nil); errors.Is(again, tc.signal) != tc.wantSignal {
				t.Fatalf("repeated retirement: %v", again)
			}
		})
	}
}

func TestOwnedRetireReportsNonpendingSignalFailureOnce(t *testing.T) {
	_, owned := exitedOwnedFixture(t)
	failure := errors.New("retained signal denial")
	owned.err = failure
	err := owned.retire(nil)
	if !errors.Is(err, failure) || strings.Count(err.Error(), failure.Error()) != 1 {
		t.Fatalf("signal failure duplicated or lost: %v", err)
	}
}
