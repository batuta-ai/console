package session

import (
	"context"
	"errors"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTask43WindowsNativeGracefulShutdownDrain(t *testing.T) {
	for _, mode := range []string{"cooperate", "unresponsive"} {
		t.Run(mode, func(t *testing.T) {
			env, home, err := accounts.SessionEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			m := New(t.TempDir())
			runner := NativeRunner{Manager: m, Environment: env, Graceful: true}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.Shutdown(ctx); err != nil && !(mode == "unresponsive" && errors.Is(err, ErrNativeCleanupUnresolved)) {
					t.Error(err)
				}
			})
			foreign := task43ForeignSibling(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			output := &task43LifetimeOutput{ready: make(chan struct{})}
			command := publication.Command{Executable: exe, Args: []string{"-test.run=^TestTask43WindowsUnresponsiveEngineFixture$", "--", "task43-unresponsive-engine", mode}, Observer: output}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result publication.CommandResult
				err    error
			}
			done := make(chan completion, 1)
			go func() { result, err := runner.Run(ctx, command); done <- completion{result, err} }()
			deadline, stop := context.WithTimeout(context.Background(), 6*time.Second)
			defer stop()
			select {
			case <-output.ready:
			case early := <-done:
				t.Fatal("graceful native engine did not start", early)
			case <-deadline.Done():
				t.Fatal("graceful native engine readiness timed out", deadline.Err())
			}
			child := task43ObserveLifetimeChild(t, output.output())
			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- runner.Manager.Shutdown(deadline) }()
			select {
			case finished := <-done:
				if !errors.Is(finished.err, context.Canceled) || finished.result.ExitCode != -1 {
					t.Fatal("cancelled native engine reported success", finished)
				}
				if mode == "cooperate" {
					if errors.Is(finished.err, ErrNativeCleanupUnresolved) || !strings.Contains(string(finished.result.Stdout), "ENGINE-WORKERS-DRAINED") || string(finished.result.Stderr) != "ENGINE-FINALIZED" {
						t.Fatal("cooperative native engine was killed before finalization", finished)
					}
				} else {
					if !errors.Is(finished.err, ErrNativeCleanupUnresolved) || !errors.Is(finished.err, publication.ErrReviewCleanupUnresolved) || strings.Contains(string(finished.result.Stdout), "ENGINE-WORKERS-DRAINED") {
						t.Fatal("uncooperative engine cleanup was guessed complete", finished)
					}
					if _, err := runner.Manager.ReserveNative(map[string]int{"account": 1}); !errors.Is(err, ErrProbeClosing) {
						t.Fatal("uncertain engine finalization left admission open", err)
					}
				}
			case <-deadline.Done():
				t.Fatal("graceful native cancellation did not finish", deadline.Err())
			}
			if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("graceful native engine worker survived", status, err)
			}
			task43SiblingStillRunning(t, foreign)
			// Uncertain engine finalization must survive the manager drain result.
			err = <-shutdownDone
			if mode == "cooperate" && err != nil || mode == "unresponsive" && !errors.Is(err, ErrNativeCleanupUnresolved) {
				t.Fatal("native engine drain lost its finalization status", err)
			}
		})
	}
}
