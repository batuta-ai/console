package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsNativeCommandLifetimeFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-command-lifetime" {
		return
	}
	argv := task43ProbeArgv(t, "hold")
	child := exec.Command(argv[0], argv[1:]...)
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
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
	if os.Args[4] == "natural" {
		os.Exit(0)
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	if status, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal(status, err)
	}
	os.Exit(0)
}

type task43LifetimeOutput struct {
	mu    sync.Mutex
	bytes bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *task43LifetimeOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.bytes.Write(data)
	if strings.Contains(w.bytes.String(), "LIFETIME-READY") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *task43LifetimeOutput) output() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes.String()
}

func TestTask43WindowsNativeCommandOwnedCancellation(t *testing.T) {
	for _, action := range []string{"cancel", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			runner := task43WindowsNativeRunner(t)
			foreign := task43ForeignSibling(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			output := &task43LifetimeOutput{ready: make(chan struct{})}
			command := publication.Command{Executable: exe, Args: []string{"-test.run=^TestTask43WindowsNativeCommandLifetimeFixture$", "--", "task43-command-lifetime", "hold"}, Observer: output}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completed := make(chan struct {
				result publication.CommandResult
				err    error
			}, 1)
			go func() {
				result, err := runner.Run(ctx, command)
				completed <- struct {
					result publication.CommandResult
					err    error
				}{result, err}
			}()
			deadline, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			select {
			case <-output.ready:
			case early := <-completed:
				t.Fatal("owned native command exited before birth receipt", early)
			case <-deadline.Done():
				t.Fatal("owned native command did not publish birth receipt", deadline.Err())
			}
			child := task43ObserveLifetimeChild(t, output.output())
			if action == "cancel" {
				cancel()
			} else if err := runner.Manager.Shutdown(deadline); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-completed:
				if action == "cancel" && (!errors.Is(result.err, context.Canceled) || result.result.ExitCode != -1) {
					t.Fatal("cancelled native command reported successful completion", result)
				}
				if action == "shutdown" && (result.err != nil || result.result.ExitCode == 0) {
					t.Fatal("terminated native command lost its nonzero outcome", result)
				}
			case <-deadline.Done():
				t.Fatal("owned command did not join complete cleanup", deadline.Err())
			}
			if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("native command child survived bounded completion", status, err)
			}
			task43SiblingStillRunning(t, foreign)
			runner.Manager.mu.Lock()
			remaining := len(runner.Manager.probes)
			runner.Manager.mu.Unlock()
			if remaining != 0 {
				t.Fatal("native command retained owned process admission", remaining)
			}
		})
	}
}
