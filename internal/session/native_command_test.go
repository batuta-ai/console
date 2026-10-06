//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
)

func nativeProbeCommand(t *testing.T, mode string, args ...string) publication.Command {
	t.Helper()
	argv := probeArgs(t, mode, args...)
	return publication.Command{Executable: argv[0], Args: argv[1:]}
}

func TestNativeCommandGracefulCancellationLetsEngineDrainWorkers(t *testing.T) {
	m := New(t.TempDir())
	marker := filepath.Join(t.TempDir(), "engine")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (NativeRunner{Manager: m, Environment: []string{"HOME=" + t.TempDir()}, Graceful: true}).Run(ctx, publication.Command{Executable: "/bin/sh", Args: []string{"-c", `trap 'printf drained > "$1.drained"; exit 0' TERM; printf ready > "$1"; while :; do /bin/sleep 0.01; done`, "sh", marker}})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("native engine did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native engine cancellation did not finish")
	}
	if bytes, err := os.ReadFile(marker + ".drained"); err != nil || string(bytes) != "drained" {
		t.Fatal("engine was killed before its worker drain", string(bytes), err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal("completed graceful drain remained uncertain", err)
	}
}

func TestNativeEscapedDescriptorCannotBecomeSuccessfulExecutorResult(t *testing.T) {
	m := New(t.TempDir())
	marker := filepath.Join(t.TempDir(), "escaped")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := nativeProbeCommand(t, "escaped-tree", marker)
	result, err := (executor.Subprocess{Runner: NativeRunner{Manager: m, Environment: []string{"HOME=" + t.TempDir()}}}).Execute(ctx, executor.Adapter{Name: "codex", Finished: "exit_code"}, executor.Invocation{Executable: command.Executable, Args: command.Args}, 0)
	pid := waitProbePID(t, marker)
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			observation, err := InspectProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Missing {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("self-terminating escaped fixture did not exit")
	})
	if result.Finished || result.ExitCode >= 0 || !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatal("unresolved native descriptor became successful execution", result, err)
	}
	if _, err := m.ReserveNative(map[string]int{"account": 1}); !errors.Is(err, ErrProbeClosing) {
		t.Fatal("unresolved native cleanup left admission open", err)
	}
	if err := m.Shutdown(ctx); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatal("unresolved native cleanup disappeared from drain", err)
	}
}

func TestNativeCommandClosedEnvironmentAndFailureOutput(t *testing.T) {
	m := New(t.TempDir())
	home := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "ambient-credential-must-not-cross")
	runner := NativeRunner{Manager: m, Environment: []string{"HOME=" + home}}
	result, err := runner.Run(context.Background(), nativeProbeCommand(t, "print", "literal"))
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
	var values map[string]string
	if err := json.Unmarshal(result.Stdout, &values); err != nil {
		t.Fatal(err)
	}
	if values["home"] != home || values["secret"] != "" || values["arg"] != "literal" {
		t.Fatal(values)
	}
	result, err = runner.Run(context.Background(), nativeProbeCommand(t, "exit"))
	if err != nil || result.ExitCode != 44 || string(result.Stdout) != "stdout-secret-on-failure" || string(result.Stderr) != "stderr-secret-on-failure" {
		t.Fatal(result, err)
	}
}

func TestNativeCommandBoundsOutputAndRetainsObserver(t *testing.T) {
	m := New(t.TempDir())
	runner := NativeRunner{Manager: m, Environment: []string{"HOME=" + t.TempDir()}}
	command := nativeProbeCommand(t, "large-stdout")
	var observed bytes.Buffer
	command.StdoutLimit, command.Observer = 128, &observed
	result, err := runner.Run(context.Background(), command)
	if err != nil || result.ExitCode != 0 || len(result.Stdout) != 128 || !result.StdoutTruncated || observed.Len() != 64<<10 {
		t.Fatal(result, observed.Len(), err)
	}
}

func TestNativeCommandCancellationDrainsOriginalGroup(t *testing.T) {
	m := New(t.TempDir())
	runner := NativeRunner{Manager: m, Environment: []string{"HOME=" + t.TempDir()}}
	marker := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, nativeProbeCommand(t, "tree", marker)); done <- err }()
	pid := waitProbePID(t, marker)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native command did not drain")
	}
	drain, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err := m.Shutdown(drain); err != nil {
		t.Fatal(err)
	}
	if observed, err := InspectProcess(pid); err != nil || !observed.Missing {
		t.Fatalf("native descendant survived completed drain: %v %v", observed, err)
	}
}

func TestNativeAdmissionSharesInteractiveAccountCapacity(t *testing.T) {
	m := New(t.TempDir())
	release, err := m.ReserveNative(map[string]int{"selected": 1})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := m.StartPending(domain.Session{ID: "blocked", AccountID: "selected", Argv: []string{"/bin/sh", "-c", "exit 0"}}, []string{"HOME=" + t.TempDir()}, "", 1); err == nil {
		t.Fatal("interactive start ignored native reservation")
	}
	if _, err := m.ReserveNative(map[string]int{"selected": 1, "other": 1}); err == nil {
		t.Fatal("native reservation ignored capacity")
	}
	release()
	second, err := m.ReserveNative(map[string]int{"other": 1})
	if err != nil {
		t.Fatal("failed reservation leaked unrelated capacity", err)
	}
	second()
	drain, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(drain); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveNative(map[string]int{"selected": 1}); !errors.Is(err, ErrProbeClosing) {
		t.Fatal("shutdown accepted native operation", err)
	}
	_, err = (NativeRunner{Manager: m, Environment: []string{"HOME=" + t.TempDir()}}).Run(context.Background(), nativeProbeCommand(t, "print", "blocked"))
	if !errors.Is(err, ErrProbeClosing) {
		t.Fatal("shutdown accepted native process", err)
	}
}
