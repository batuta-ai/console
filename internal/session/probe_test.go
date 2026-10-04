//go:build darwin || linux

package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func probeArgs(t *testing.T, mode string, args ...string) []string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{path, "-test.run=^TestProbeProcessHelper$", "--", mode}, args...)
}

func TestProbeProcessHelper(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "print":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"home": os.Getenv("HOME"), "secret": os.Getenv("ANTHROPIC_API_KEY"), "arg": args[1]})
		fmt.Fprint(os.Stderr, "stderr-sentinel-must-not-be-returned")
	case "exit":
		fmt.Fprint(os.Stdout, "stdout-secret-on-failure")
		fmt.Fprint(os.Stderr, "stderr-secret-on-failure")
		os.Exit(44)
	case "large-stdout":
		fmt.Fprint(os.Stdout, strings.Repeat("s", 64<<10))
	case "large-stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("s", 64<<10))
	case "combined-output":
		fmt.Fprint(os.Stdout, strings.Repeat("s", 20<<10))
		fmt.Fprint(os.Stderr, strings.Repeat("s", 20<<10))
	case "escaped-holder":
		if err := publishProbePID(args); err != nil {
			os.Exit(91)
		}
		time.Sleep(1500 * time.Millisecond)
	case "escaped-tree":
		child := exec.Command(os.Args[0], "-test.run=^TestProbeProcessHelper$", "--", "escaped-holder", args[1])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(92)
		}
		if err := waitProbePublication(args[1]); err != nil {
			drainFailedProbeChild(child)
			os.Exit(91)
		}
		os.Exit(0)
	case "hold":
		if err := publishProbePID(args); err != nil {
			os.Exit(91)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "tree", "natural-tree", "overflow-tree":
		child := exec.Command(os.Args[0], "-test.run=^TestProbeProcessHelper$", "--", "hold", args[1])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(92)
		}
		if err := waitProbePublication(args[1]); err != nil {
			drainFailedProbeChild(child)
			os.Exit(91)
		}
		if args[0] == "natural-tree" {
			os.Exit(0)
		}
		if args[0] == "overflow-tree" {
			fmt.Fprint(os.Stdout, strings.Repeat("s", 64<<10))
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(0)
}

// The optional pipe barrier holds the real producer before it commits readiness.
func publishProbePID(args []string) error {
	file, err := os.CreateTemp(filepath.Dir(args[1]), ".probe-pid-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if len(args) > 2 && args[2] == "publication-barrier" {
		if _, err := os.Stdout.Write([]byte{1}); err != nil {
			return err
		}
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
			return err
		}
	}
	return os.Rename(file.Name(), args[1])
}

func drainFailedProbeChild(child *exec.Cmd) {
	done := make(chan struct{})
	go func() { _ = child.Wait(); close(done) }()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = child.Process.Kill()
		<-done
	}
}

func waitProbePublication(marker string) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(marker)
		if err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("invalid fixture marker: %s", marker)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("probe child did not publish readiness")
}

func TestProbePIDPublicationFailure(t *testing.T) {
	for _, mode := range []string{"hold", "escaped-holder", "tree", "natural-tree", "overflow-tree", "escaped-tree"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "pid")
			if err := os.Mkdir(marker, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			argv := probeArgs(t, mode, marker)
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 91 {
				t.Fatalf("publication failure: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "pid" {
				t.Fatalf("failed publication artifacts: %v %v", entries, err)
			}
		})
	}
}

func TestProbePIDPublication(t *testing.T) {
	for _, mode := range []string{"hold", "escaped-holder"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "pid")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			argv := probeArgs(t, mode, marker, "publication-barrier")
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			var ready [1]byte
			if _, err := io.ReadFull(output, ready[:]); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(marker); !os.IsNotExist(err) {
				t.Fatalf("marker visible before publication commit: %q %v", data, err)
			}
			if _, err := input.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			if pid := waitProbePID(t, marker); pid != cmd.Process.Pid {
				t.Fatalf("published PID %d differs from owned producer %d", pid, cmd.Process.Pid)
			}
			info, err := os.Stat(marker)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("marker permissions: %v %v", info, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("publication artifacts: %v %v", entries, err)
			}
		})
	}
}

func probeManager(t *testing.T) (*Manager, string, *atomic.Int32) {
	t.Helper()
	logs := t.TempDir()
	m := New(logs)
	callbacks := &atomic.Int32{}
	m.OnExit = func(_ domain.Session) { callbacks.Add(1) }
	return m, logs, callbacks
}

func waitProbePID(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, err := strconv.Atoi(string(data))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid owned fixture PID: %q %v", data, err)
			}
			return pid
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("probe child did not acknowledge startup")
	return 0
}

func TestProbeMemoryOnlyClosedEnvironment(t *testing.T) {
	m, logs, callbacks := probeManager(t)
	t.Setenv("ANTHROPIC_API_KEY", "daemon-secret-must-not-pass")
	literal := "--model; $(literal) `literal`\nline"
	result, err := m.Probe(context.Background(), probeArgs(t, "print", literal), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(result.Output, &got); err != nil {
		t.Fatal(err)
	}
	if got["secret"] != "" || got["arg"] != literal || got["home"] == "" || got["home"] == os.Getenv("HOME") {
		t.Fatalf("probe received wrong closed environment or argv: %v", got)
	}
	if _, err := os.Stat(got["home"]); !os.IsNotExist(err) {
		t.Fatalf("probe private home not removed: %v", err)
	}
	entries, err := os.ReadDir(logs)
	if err != nil || len(entries) != 0 {
		t.Fatalf("diagnostic transcript exists: %v %v", entries, err)
	}
	m.mu.Lock()
	liveCount := len(m.live)
	m.mu.Unlock()
	if callbacks.Load() != 0 || liveCount != 0 {
		t.Fatal("diagnostic appeared as a session or called OnExit")
	}
}

func TestProbeErrorsDiscardOutput(t *testing.T) {
	for _, mode := range []string{"exit", "large-stdout", "large-stderr", "combined-output"} {
		t.Run(mode, func(t *testing.T) {
			m, logs, _ := probeManager(t)
			result, err := m.Probe(context.Background(), probeArgs(t, mode), nil, "")
			if err == nil || len(result.Output) != 0 || strings.Contains(err.Error(), "secret-on-failure") {
				t.Fatalf("probe failure leaked output or succeeded: code=%d bytes=%d err=%v", result.ExitCode, len(result.Output), err)
			}
			if mode == "exit" && (result.ExitCode != 44 || !errors.Is(err, ErrProbeExit)) {
				t.Fatalf("exit status lost: %+v %v", result, err)
			}
			if mode != "exit" && !errors.Is(err, ErrProbeOutputLimit) {
				t.Fatalf("output limit cause lost: %v", err)
			}
			if entries, err := os.ReadDir(logs); err != nil || len(entries) != 0 {
				t.Fatalf("failed diagnostic transcript: %v %v", entries, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := m.Shutdown(ctx); err != nil {
				t.Fatalf("normal diagnostic error contaminated cleanup: %v", err)
			}
		})
	}
}

func TestProbePreservesExplicitPrivateHome(t *testing.T) {
	m, logs, _ := probeManager(t)
	home := t.TempDir()
	marker := filepath.Join(home, "owned-by-caller")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Probe(context.Background(), probeArgs(t, "print", "explicit"), []string{"HOME=" + home}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(result.Output, &got); err != nil || got["home"] != home {
		t.Fatalf("explicit private home not selected: %v %v", got, err)
	}
	if strings.Contains(string(result.Output), "stderr-sentinel") {
		t.Fatal("stderr returned as successful stdout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatalf("caller-owned home was removed or changed: %q %v", data, err)
	}
	if entries, err := os.ReadDir(logs); err != nil || len(entries) != 0 {
		t.Fatalf("explicit-home diagnostic transcript: %v %v", entries, err)
	}
}

func TestProbeEscapedDescriptorBoundWithoutAdoption(t *testing.T) {
	m, _, _ := probeManager(t)
	marker := filepath.Join(t.TempDir(), "escaped-child")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result, err := m.Probe(ctx, probeArgs(t, "escaped-tree", marker), nil, "")
	if !errors.Is(err, exec.ErrWaitDelay) || len(result.Output) != 0 {
		t.Fatalf("inherited escaped pipe did not report bounded drain failure: %+v %v", result, err)
	}
	pid := waitProbePID(t, marker)
	if observed, err := InspectProcess(pid); err != nil || observed.Missing {
		t.Fatalf("escaped fixture was adopted or killed: %v %v", observed, err)
	}
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("completed pipe descriptor failure poisoned empty drain: %v", err)
	}
	// The disposable escaped fixture terminates itself; no new kill authority.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		observed, err := InspectProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Missing {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("self-terminating escaped fixture did not exit")
}

func TestProbeInvalidAndCancelledDoNotSpawn(t *testing.T) {
	m, _, _ := probeManager(t)
	if _, err := m.Probe(context.Background(), nil, nil, ""); !errors.Is(err, ErrInvalidProbe) {
		t.Fatalf("empty command: %v", err)
	}
	if _, err := m.Probe(context.Background(), []string{filepath.Join(t.TempDir(), "missing")}, nil, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spawn cause lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	marker := filepath.Join(t.TempDir(), "pid")
	if _, err := m.Probe(ctx, probeArgs(t, "hold", marker), nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel cause lost: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled diagnostic executed: %v", err)
	}
}

func TestProbeEnvironmentFailureReleasesAdmission(t *testing.T) {
	m, _, _ := probeManager(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	result, err := m.Probe(context.Background(), probeArgs(t, "print", "must-not-spawn"), nil, "")
	if !errors.Is(err, os.ErrNotExist) || len(result.Output) != 0 {
		t.Fatalf("environment failure was lost: %+v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("failed environment preparation retained admission: %v", err)
	}
}

func TestProbeOverflowDrainOriginalDescendant(t *testing.T) {
	m, _, _ := probeManager(t)
	marker := filepath.Join(t.TempDir(), "child")
	result, err := m.Probe(context.Background(), probeArgs(t, "overflow-tree", marker), nil, "")
	if !errors.Is(err, ErrProbeOutputLimit) || len(result.Output) != 0 {
		t.Fatalf("overflow descendant result: %+v %v", result, err)
	}
	pid := waitProbePID(t, marker)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if observed, err := InspectProcess(pid); err != nil || !observed.Missing {
		t.Fatalf("overflow descendant survived completed drain: %v %v", observed, err)
	}
}

func TestProbeDeadlineAndNaturalExitDrainGroup(t *testing.T) {
	for _, mode := range []string{"tree", "natural-tree"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := probeManager(t)
			marker := filepath.Join(t.TempDir(), "child")
			budget := time.Second
			if mode == "natural-tree" {
				// Race-instrumented helpers have Go's default 1000 ms exit delay.
				budget = 3 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			_, err := m.Probe(ctx, probeArgs(t, mode, marker), nil, "")
			if mode == "tree" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline not reported: %v", err)
			}
			if mode == "natural-tree" && err != nil {
				t.Fatal(err)
			}
			pid := waitProbePID(t, marker)
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer drainCancel()
			if err := m.Shutdown(drainCtx); err != nil {
				t.Fatal(err)
			}
			observed, err := InspectProcess(pid)
			if err != nil || !observed.Missing {
				t.Fatalf("original-group descendant survived completed drain: %v %v", observed, err)
			}
		})
	}
}

func TestProbeShutdownDrainsAndClosesAdmission(t *testing.T) {
	m, _, callbacks := probeManager(t)
	marker := filepath.Join(t.TempDir(), "pid")
	done := make(chan error, 1)
	argv := probeArgs(t, "hold", marker)
	go func() {
		_, err := m.Probe(context.Background(), argv, nil, "")
		done <- err
	}()
	pid := waitProbePID(t, marker)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrProbeExit) {
			t.Fatalf("shutdown lost terminated probe result: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown left diagnostic waiter running")
	}
	if observed, err := InspectProcess(pid); err != nil || !observed.Missing {
		t.Fatalf("probe survived shutdown: %v %v", observed, err)
	}
	if _, err := m.Probe(context.Background(), probeArgs(t, "print", "after-close"), nil, ""); !errors.Is(err, ErrProbeClosing) {
		t.Fatalf("new probe admitted during shutdown: %v", err)
	}
	m.mu.Lock()
	liveCount := len(m.live)
	m.mu.Unlock()
	if callbacks.Load() != 0 || liveCount != 0 {
		t.Fatal("diagnostic published a session")
	}
}

func TestProbeStartShutdownRace(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		m, _, _ := probeManager(t)
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make(chan error, 4)
		argv := probeArgs(t, "print", "race")
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := m.Probe(context.Background(), argv, nil, "")
				results <- err
			}()
		}
		close(start)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := m.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil && !errors.Is(err, ErrProbeClosing) && !errors.Is(err, ErrProbeExit) {
				t.Fatalf("unexpected native race failure: %v", err)
			}
		}
		m.mu.Lock()
		left := len(m.probes)
		m.mu.Unlock()
		if left != 0 {
			t.Fatalf("probe ownership left after shutdown: %d", left)
		}
	}
}
