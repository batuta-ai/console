package hostfs

import (
	"errors"
	"os/exec"
	"runtime"
	"testing"
)

func TestNormalizePicked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix trim")
	}
	got := normalizePicked("  /tmp/acme/  \n")
	if got != "/tmp/acme" {
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
	err := exec.Command("false").Run()
	if !isCanceled(err, "") {
		t.Fatalf("exit 1 %+v", err)
	}
}
