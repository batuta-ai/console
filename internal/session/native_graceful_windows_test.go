package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsGracefulEngineFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-graceful-engine" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if os.Args[4] == "ignore" {
		signal.Ignore(os.Interrupt)
	}
	argv := task43ProbeArgv(t, "hold")
	child := exec.Command(argv[0], argv[1:]...)
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	var created, exited, kernel, user windows.Filetime
	var observed error
	if err := child.Process.WithHandle(func(handle uintptr) {
		observed = windows.GetProcessTimes(windows.Handle(handle), &created, &exited, &kernel, &user)
	}); err != nil || observed != nil {
		t.Fatal(err, observed)
	}
	fmt.Fprintf(os.Stdout, "LIFETIME-CHILD:%d:%d\nLIFETIME-READY\n", child.Process.Pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	<-ctx.Done()
	if err := child.Process.Kill(); err != nil {
		t.Fatal("engine did not retain its original worker authority", err)
	}
	var exitError *exec.ExitError
	if err := child.Wait(); err != nil && !errors.As(err, &exitError) {
		t.Fatal(err)
	}
	fmt.Fprint(os.Stdout, "ENGINE-WORKERS-DRAINED")
	fmt.Fprint(os.Stderr, "ENGINE-FINALIZED")
	os.Exit(0)
}

func TestTask43WindowsNativeGracefulDrain(t *testing.T) {
	for _, mode := range []string{"cooperate", "ignore"} {
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
				if err := m.Shutdown(ctx); err != nil && !(mode == "ignore" && errors.Is(err, ErrNativeCleanupUnresolved)) {
					t.Error(err)
				}
			})
			foreign := task43ForeignSibling(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			output := &task43LifetimeOutput{ready: make(chan struct{})}
			command := publication.Command{Executable: exe, Args: []string{"-test.run=^TestTask43WindowsGracefulEngineFixture$", "--", "task43-graceful-engine", mode}, Observer: output}
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
			observation := task43ObserveGrace(t, m)
			defer observation.finish("cleanup")
			observation.phase("before-cancel")
			cancel()
			observation.phase("after-cancel")
			select {
			case finished := <-done:
				observation.finish("completion")
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
			err = runner.Manager.Shutdown(deadline)
			if mode == "cooperate" && err != nil || mode == "ignore" && !errors.Is(err, ErrNativeCleanupUnresolved) {
				t.Fatal("native engine drain lost its finalization status", err)
			}
		})
	}
}
