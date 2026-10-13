package hostfs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizePicked(t *testing.T) {
	raw, want := "  /tmp/acme/  \n", "/tmp/acme"
	if runtime.GOOS == "windows" {
		raw, want = "  C:\\tmp\\acme\\  \r\n", `C:\tmp\acme`
	}
	got := normalizePicked(raw)
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestIsCanceled(t *testing.T) {
	if !isCanceled(errors.New("osascript: User canceled. (-128)"), "") {
		t.Fatal("apple event cancel")
	}
	if isCanceled(errors.New("osascript: execution error"), "permission denied") {
		t.Fatal("real failure must not look canceled")
	}
	cmd := exec.Command("false")
	if runtime.GOOS == "windows" {
		cmd = exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/d", "/c", "exit", "1")
	}
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("fixture did not execute an exit-1 process: %v", err)
	}
	if !isCanceled(err, "") {
		t.Fatalf("exit 1 %+v", err)
	}
}
