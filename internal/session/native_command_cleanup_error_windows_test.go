package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsNativeCommandObserverFailureKeepsCleanupCause(t *testing.T) {
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	manager := New(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		end, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		manager.Shutdown(end)
	}()
	foreign := task43ForeignSibling(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	birthReader, birthWriter := io.Pipe()
	defer birthReader.Close()
	defer birthWriter.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := publication.Command{Executable: exe, Directory: t.TempDir(), Args: []string{"-test.run=^TestTask43WindowsNativeCommandIOFixture$", "--", "task43-native-command-io", "stdout", "hold"}, Observer: task43ExchangeClosedPipeSink{writer: write, release: release}, StderrObserver: birthWriter}
	runner := NativeRunner{Manager: manager, Environment: env}
	type outcome struct {
		result publication.CommandResult
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		result, err := runner.Run(ctx, command)
		completed <- outcome{result, err}
	}()
	births := make(chan task43ExchangeBirth, 1)
	birthErrors := make(chan error, 1)
	go func() {
		var birth task43ExchangeBirth
		if err := json.NewDecoder(birthReader).Decode(&birth); err != nil {
			birthErrors <- err
			return
		}
		births <- birth
	}()
	var birth task43ExchangeBirth
	select {
	case birth = <-births:
	case err := <-birthErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	root := task43ObserveLifetimeChild(t, birth.Root)
	manager.mu.Lock()
	var owned *ownedProcess
	for process := range manager.probes {
		owned = process
	}
	manager.mu.Unlock()
	if owned == nil {
		t.Fatal("actual original Job owner missing")
	}
	owned.mu.Lock()
	original := owned.job
	var restricted windows.Handle
	err = windows.DuplicateHandle(windows.CurrentProcess(), original, windows.CurrentProcess(), &restricted, windows.JOB_OBJECT_TERMINATE, false, 0)
	if err == nil {
		owned.job = restricted
	}
	owned.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(original)
	unblock()
	ended := <-completed
	if ended.result.ExitCode != -1 || !errors.Is(ended.err, windows.ERROR_NO_DATA) || !errors.Is(ended.err, windows.ERROR_ACCESS_DENIED) || !errors.Is(ended.err, ErrNativeCleanupUnresolved) || errors.Is(ended.err, context.DeadlineExceeded) {
		t.Fatal("observer classification erased actual original Job cleanup failure", ended.result.ExitCode, ended.err)
	}
	if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("original IO/Query-failed root survived return", status, err)
	}
	task43SiblingStillRunning(t, foreign)
	if result, err := runner.Run(context.Background(), task43WindowsNativeCommand(t, "nonzero")); result.ExitCode != -1 || !errors.Is(err, ErrProbeClosing) {
		t.Fatal("actual cleanup failure did not close admission", result.ExitCode, err)
	}
	if err := manager.Shutdown(ctx); !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !errors.Is(err, ErrNativeCleanupUnresolved) {
		t.Fatal("shutdown lost actual original Job cleanup uncertainty", err)
	}
}
