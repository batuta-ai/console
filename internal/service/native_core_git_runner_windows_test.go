package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/publication"
)

func TestNativeCoreGitRunnerCompletedExitContract(t *testing.T) {
	commands, home, shell, git := nativeCoreGitEnvironmentCommands(t)
	runner := nativeCoreGitRunner{commands: commands}
	init, err := commands.Run(context.Background(), publication.Command{Executable: git, Directory: home, Args: []string{"init", "--template="}})
	if err != nil || init.ExitCode != 0 {
		t.Fatal("actual fixture init", init, err)
	}
	for _, tc := range []struct {
		name       string
		executable string
		args       []string
		exit       int
		wantError  bool
	}{
		{"git-success", git, []string{"--version"}, 0, false},
		{"git-bounded-output", git, []string{"--version"}, 0, false},
		{"git-missing-setting", git, []string{"config", "--get", "fixture.absent"}, 1, true},
		{"git-failure-stderr", git, []string{"rev-parse", "--verify", "refs/heads/absent"}, 128, true},
		{"non-git-exit-remains-data", shell, []string{"-c", "printf raw-out; printf raw-err >&2; exit 23"}, 23, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := publication.Command{Executable: tc.executable, Directory: home, Args: tc.args, StdoutLimit: 128, StderrLimit: 512}
			if tc.name == "git-bounded-output" {
				command.StdoutLimit = 4
			}
			raw, rawErr := commands.Run(context.Background(), command)
			if tc.name == "git-bounded-output" && (!raw.StdoutTruncated || len(raw.Stdout) != 4) {
				t.Fatal("actual bounded Git output required", raw)
			}
			if rawErr != nil || raw.ExitCode != tc.exit {
				t.Fatalf("actual raw result=%+v error=%v", raw, rawErr)
			}
			if tc.name == "git-failure-stderr" && len(raw.Stderr) == 0 {
				t.Fatal("real failed Git stderr missing")
			}
			if tc.name == "non-git-exit-remains-data" && (string(raw.Stdout) != "raw-out" || string(raw.Stderr) != "raw-err") {
				t.Fatal("actual shell streams", raw)
			}
			result, err := runner.Run(context.Background(), command)
			if !reflect.DeepEqual(result, raw) || (err != nil) != tc.wantError {
				t.Fatalf("Core Git adapter result=%+v error=%v want result=%+v error=%v", result, err, raw, tc.wantError)
			}
			if errors.Is(err, session.ErrNativeCleanupUnresolved) {
				t.Fatal("completed exit became ownership uncertainty", err)
			}
			if err != nil && (strings.Contains(err.Error(), string(raw.Stderr)) && len(raw.Stderr) > 0) {
				t.Fatal("raw command stderr leaked into adapter error")
			}
			// A completed failure must not close admission or alter ownership state.
			next, nextErr := runner.Run(context.Background(), publication.Command{Executable: git, Directory: home, Args: []string{"--version"}})
			if nextErr != nil || next.ExitCode != 0 {
				t.Fatal("completed failure closed actual manager", next, nextErr)
			}
		})
	}
}

func TestNativeCoreGitRunnerPreservesPreCanceledContext(t *testing.T) {
	commands, home, _, git := nativeCoreGitEnvironmentCommands(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (nativeCoreGitRunner{commands: commands}).Run(ctx, publication.Command{Executable: git, Directory: home, Args: []string{"--version"}})
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatal("actual cancellation contract", result, err)
	}
	next, nextErr := (nativeCoreGitRunner{commands: commands}).Run(context.Background(), publication.Command{Executable: git, Directory: home, Args: []string{"--version"}})
	if nextErr != nil || next.ExitCode != 0 {
		t.Fatal("pre-cancellation closed actual manager", next, nextErr)
	}
}

// The signal comes from a shell launched by the actual Git alias, before exec
// replaces that shell with its child. Cancellation must join the original Job.
type nativeGitSignalWriter struct {
	ready   chan struct{}
	once    sync.Once
	pending string
}

func (w *nativeGitSignalWriter) Write(body []byte) (int, error) {
	w.pending += string(body)
	if strings.Contains(w.pending, "native-git-ready") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(body), nil
}

func TestNativeCoreGitRunnerPreservesRunningCancellation(t *testing.T) {
	commands, home, _, git := nativeCoreGitEnvironmentCommands(t)
	runner := nativeCoreGitRunner{commands: commands}
	init, err := runner.Run(context.Background(), publication.Command{Executable: git, Directory: home, Args: []string{"init", "--template="}})
	if err != nil || init.ExitCode != 0 {
		t.Fatal("actual cancellation fixture init", init, err)
	}
	observer := &nativeGitSignalWriter{ready: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result publication.CommandResult
		err    error
	}
	done := make(chan outcome, 1)
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
				joined = true
			case <-time.After(10 * time.Second):
				t.Error("actual Git command did not join during test cleanup")
			}
		}
	})
	go func() {
		result, err := runner.Run(ctx, publication.Command{Executable: git, Directory: home, Args: []string{"-c", "alias.wait=!printf native-git-ready; exec sleep 60", "wait"}, Observer: observer})
		done <- outcome{result, err}
	}()
	select {
	case <-observer.ready:
	case got := <-done:
		joined = true
		t.Fatalf("actual Git exited before readiness: %+v error=%v", got.result, got.err)
	case <-time.After(10 * time.Second):
		t.Fatal("actual running Git readiness unavailable")
	}
	cancel()
	var got outcome
	select {
	case got = <-done:
		joined = true
	case <-time.After(10 * time.Second):
		t.Fatal("actual canceled Git did not join")
	}
	if !errors.Is(got.err, context.Canceled) || errors.Is(got.err, session.ErrNativeCleanupUnresolved) || got.result.ExitCode != -1 || !strings.Contains(string(got.result.Stdout), "native-git-ready") {
		t.Fatal("actual running Git cancellation contract", got.result, got.err)
	}
	next, nextErr := runner.Run(context.Background(), publication.Command{Executable: git, Directory: home, Args: []string{"--version"}})
	if nextErr != nil || next.ExitCode != 0 {
		t.Fatal("running cancellation closed actual manager", next, nextErr)
	}
}
