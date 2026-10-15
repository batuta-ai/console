package testfixture_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestTerminalEnvironmentMSYSTempLifetime(t *testing.T) {
	parent := testfixture.PrivateHome(t)
	dir, err := consolert.OpenPrivateDirectory(parent)
	if err != nil {
		t.Fatal("owned protected harness temporary root", err)
	}
	parentInfo, statErr := dir.Stat()
	closeErr := dir.Close()
	if statErr != nil || closeErr != nil {
		t.Fatal(statErr, closeErr)
	}
	t.Setenv("TEMP", parent)
	t.Setenv("TMP", parent)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	var keeper *exec.Cmd
	var input io.WriteCloser
	var output *bufio.Reader
	var stderr bytes.Buffer
	// Join the original creation handle before restoring the environment or removing parent.
	t.Cleanup(func() {
		if input != nil {
			if err := input.Close(); err != nil {
				t.Error("owned MSYS keeper EOF", err)
			}
		}
		if keeper != nil {
			if err := keeper.Wait(); err != nil {
				t.Error("owned MSYS keeper completion", err)
			}
			if stderr.Len() != 0 {
				t.Errorf("owned MSYS keeper stderr=%q", stderr.String())
			}
		}
	})
	assertAlive := func(t *testing.T) {
		t.Helper()
		var status uint32
		var waitErr error
		if err := keeper.Process.WithHandle(func(handle uintptr) {
			status, waitErr = windows.WaitForSingleObject(windows.Handle(handle), 0)
		}); err != nil || waitErr != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("original owned MSYS keeper is not alive: status=%d errors=%v/%v", status, err, waitErr)
		}
	}
	observe := func(t *testing.T) map[string]string {
		t.Helper()
		paths := make(map[string]string)
		for _, key := range []string{"/tmp", "HOME", "TEMP", "TMP", "ready"} {
			line, err := output.ReadString('\n')
			if err != nil {
				t.Fatalf("owned MSYS observation %s: %v", key, err)
			}
			paths[key] = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		}
		if paths["ready"] != "MSYS-TEMP-READY" {
			t.Fatalf("owned MSYS readiness=%q", paths)
		}
		assertAlive(t)
		return paths
	}
	var firstHome string
	var selected, before map[string]string
	if !t.Run("first_scope", func(t *testing.T) {
		firstHome = testfixture.PrivateHome(t)
		environment := testfixture.TerminalEnvironment(t, []string{"HOME=" + firstHome, "PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"})
		selected = make(map[string]string)
		for _, entry := range environment {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				selected[strings.ToUpper(key)] = value
			}
		}
		if selected["HOME"] != firstHome {
			t.Fatalf("fixture changed selected HOME: got=%q want=%q", selected["HOME"], firstHome)
		}
		argv := testfixture.Argv(t, []string{"/bin/sh", "--noprofile", "--norc", "-p", "-c", `set -eu
report() {
    cygpath -w /tmp
    cygpath -w "$HOME"
    cygpath -w "$TEMP"
    cygpath -w "$TMP"
    printf '%s\n' MSYS-TEMP-READY
}
report
while IFS= read -r request; do
    case "$request" in
        inspect) report ;;
        *) exit 91 ;;
    esac
done
`})
		command := exec.CommandContext(ctx, argv[0], argv[1:]...)
		command.Dir, command.Env, command.Stderr = parent, environment, &stderr
		command.WaitDelay = 5 * time.Second
		var err error
		input, err = command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		pipe, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		output = bufio.NewReader(pipe)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		keeper = command
		before = observe(t)
		for _, key := range []string{"HOME", "TEMP", "TMP"} {
			actual, actualErr := os.Stat(before[key])
			want, wantErr := os.Stat(selected[key])
			if !filepath.IsAbs(selected[key]) || actualErr != nil || wantErr != nil || !actual.IsDir() || !want.IsDir() || !os.SameFile(actual, want) {
				t.Fatalf("actual MSYS %s=%q selected=%q errors=%v/%v", key, before[key], selected[key], actualErr, wantErr)
			}
		}
		backing, err := os.Stat(before["/tmp"])
		if err != nil || !backing.IsDir() {
			t.Fatalf("actual MSYS /tmp backing before cleanup=%q: %v", before["/tmp"], err)
		}
		t.Logf("before cleanup: actual MSYS paths=%q selected HOME=%q TEMP=%q TMP=%q parent=%q", before, firstHome, selected["TEMP"], selected["TMP"], parent)
	}) {
		t.Fatal("first owned MSYS scope did not establish readiness")
	}
	assertAlive(t)
	if _, err := os.Stat(firstHome); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("selected first-scope HOME survived cleanup: %q error=%v", firstHome, err)
	}
	if _, err := io.WriteString(input, "inspect\n"); err != nil {
		t.Fatal("owned MSYS observation after cleanup", err)
	}
	after := observe(t)
	t.Logf("after cleanup: actual MSYS paths=%q selected TEMP=%q TMP=%q parent=%q", after, selected["TEMP"], selected["TMP"], parent)
	for key, path := range before {
		if after[key] != path {
			t.Errorf("owned MSYS %s changed across cleanup: before=%q after=%q", key, path, after[key])
		}
	}
	// A valid pre-existing shared /tmp mapping must not mask a per-test TEMP lifetime.
	for _, key := range []string{"TEMP", "TMP"} {
		info, err := os.Stat(selected[key])
		if err != nil || !info.IsDir() {
			t.Errorf("fixture %s did not survive first-scope cleanup: %q error=%v", key, selected[key], err)
		} else if !os.SameFile(parentInfo, info) {
			t.Errorf("fixture %s does not retain the parent harness root: selected=%q parent=%q", key, selected[key], parent)
		}
	}
	for _, path := range []string{before["/tmp"], after["/tmp"]} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("actual MSYS /tmp backing disappeared across cleanup: %q error=%v", path, err)
		}
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() || !os.SameFile(parentInfo, info) {
		t.Errorf("owned parent harness temporary root changed: %q error=%v", parent, err)
	}
	t.Run("later_original_literal", TestNativeScriptLiteralArgumentsAndExit)
	assertAlive(t)
}
