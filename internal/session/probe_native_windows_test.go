package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func task43SessionHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "session home ç")
	if err := consoleruntime.EnsurePrivateDirectory(home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	return home
}

func task43ProbeArgv(t *testing.T, mode string, extra ...string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{exe, "-test.run=^TestTask43WindowsProbeProcessFixture$", "--", "task43-probe-fixture", mode}, extra...)
}

func TestTask43WindowsProbeProcessFixture(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "task43-probe-fixture" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	switch os.Args[at+1] {
	case "environment":
		var inJob bool
		if err := windows.IsProcessInJob(windows.CurrentProcess(), 0, &inJob); err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				values[strings.ToUpper(key)] = value
			}
		}
		out := struct {
			Environment map[string]string `json:"environment"`
			Arg         string            `json:"arg"`
			InJob       bool              `json:"in_job"`
		}{values, os.Args[at+2], inJob}
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "nonzero":
		fmt.Fprint(os.Stdout, "discarded stdout")
		fmt.Fprint(os.Stderr, "discarded stderr")
		os.Exit(7)
	case "overflow":
		fmt.Fprint(os.Stdout, strings.Repeat("x", (32<<10)+1))
		os.Exit(0)
	case "hold":
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(event)
		if status, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal("fixture wait", status, err)
		}
		os.Exit(0)
	case "descendant":
		child := exec.Command(os.Args[0], "-test.run=^TestTask43WindowsProbeProcessFixture$", "--", "task43-probe-fixture", "hold")
		child.Env = os.Environ()
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		var created, exited, kernel, user windows.Filetime
		var inspectErr error
		if err := child.Process.WithHandle(func(handle uintptr) {
			inspectErr = windows.GetProcessTimes(windows.Handle(handle), &created, &exited, &kernel, &user)
		}); err != nil || inspectErr != nil {
			t.Fatal("creation handle", err, inspectErr)
		}
		stamp := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
		fmt.Fprintf(os.Stdout, "%d:%d\nROOT-EXIT\n", child.Process.Pid, stamp)
		os.Exit(0)
	default:
		t.Fatal("invalid owned fixture mode")
	}
}

func TestTask43WindowsProbeNativeEnvironment(t *testing.T) {
	home := task43SessionHome(t)
	m := New(filepath.Join(home, "logs"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	t.Setenv("OPENAI_API_KEY", "synthetic-must-not-inherit")
	t.Setenv("CLAUDE_CONFIG_DIR", "foreign")
	literal := "  quotes\" ; $(literal)\nç "
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result, err := m.Probe(ctx, task43ProbeArgv(t, "environment", literal), nil, home)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("native probe: %+v error=%v", result, err)
	}
	var got struct {
		Environment map[string]string `json:"environment"`
		Arg         string            `json:"arg"`
		InJob       bool              `json:"in_job"`
	}
	if err := json.Unmarshal(result.Output, &got); err != nil {
		t.Fatal(err)
	}
	if got.Arg != literal || !got.InJob {
		t.Fatalf("argv or kernel job lost: arg=%q in_job=%v", got.Arg, got.InJob)
	}
	if got.Environment["OPENAI_API_KEY"] != "" || got.Environment["CLAUDE_CONFIG_DIR"] == "foreign" {
		t.Fatal("probe inherited daemon credentials")
	}
	selected := got.Environment["HOME"]
	if selected == "" || selected == home || got.Environment["USERPROFILE"] != selected || got.Environment["SYSTEMROOT"] == "" {
		t.Fatal("native probe lacks closed identity/OS environment")
	}
	if _, err := os.Lstat(selected); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe retained owned credentialless home", err)
	}
}

func TestTask43WindowsProbeFailureBounds(t *testing.T) {
	home := task43SessionHome(t)
	m := New(filepath.Join(home, "logs"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	for _, test := range []struct {
		mode string
		want error
	}{{"nonzero", ErrProbeExit}, {"overflow", ErrProbeOutputLimit}} {
		t.Run(test.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			result, err := m.Probe(ctx, task43ProbeArgv(t, test.mode), nil, home)
			if !errors.Is(err, test.want) || len(result.Output) != 0 {
				t.Fatalf("failed probe exposed output or wrong error: %+v %v", result, err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := m.Probe(ctx, task43ProbeArgv(t, "hold"), nil, home)
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Output) != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("probe deadline not bounded: %+v error=%v elapsed=%v", result, err, time.Since(start))
	}
}

func TestTask43WindowsProbeNaturalDrainPreservesSibling(t *testing.T) {
	home := task43SessionHome(t)
	m := New(filepath.Join(home, "logs"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	argv := task43ProbeArgv(t, "hold")
	sibling := exec.Command(argv[0], argv[1:]...)
	sibling.Env = []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "USERPROFILE=" + home}
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { sibling.Process.Kill(); sibling.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := m.Probe(ctx, task43ProbeArgv(t, "descendant"), nil, home)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("natural descendant drain: %+v error=%v", result, err)
	}
	line, tail, ok := strings.Cut(string(result.Output), "\n")
	if !ok || tail != "ROOT-EXIT\n" {
		t.Fatal("descendant fixture output", string(result.Output))
	}
	pidText, stampText, ok := strings.Cut(line, ":")
	if !ok {
		t.Fatal("creation receipt")
	}
	pid, err := strconv.ParseUint(pidText, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := strconv.ParseUint(stampText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// Observation only: a reused PID must never become control authority.
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(handle)
		var created, exited, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
			t.Fatal(err)
		}
		if uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) == stamp {
			status, err := windows.WaitForSingleObject(handle, 0)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("owned descendant survived natural root exit", status, err)
			}
		}
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatal("descendant observation", err)
	}
	var status uint32
	var observeErr error
	if err := sibling.Process.WithHandle(func(handle uintptr) { status, observeErr = windows.WaitForSingleObject(windows.Handle(handle), 0) }); err != nil || observeErr != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("foreign sibling affected", status, err, observeErr)
	}
}
