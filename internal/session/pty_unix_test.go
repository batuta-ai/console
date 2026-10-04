//go:build darwin || linux

package session

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
)

func TestPTYObservationClosesDescriptors(t *testing.T) {
	directory := "/dev/fd"
	if runtime.GOOS == "linux" {
		directory = "/proc/self/fd"
	}
	before, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got := ObservePTY(context.Background())
		if !got.Supported || !got.Available || got.Code != "pty_available" || got.Qualification.Status != "pending" {
			t.Fatalf("native PTY: %+v", got)
		}
	}
	after, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("descriptors before=%d after=%d", len(before), len(after))
	}
}

func TestPTYObservationCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := ObservePTY(ctx)
	if !got.Supported || got.Available || got.Code != "check_cancelled" {
		t.Fatalf("%+v", got)
	}
}

func TestPTYObservationUnavailable(t *testing.T) {
	const helper = "BATUTA_PTY_OBSERVATION_TEST"
	if os.Getenv(helper) == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		limit.Cur = 0
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		got := ObservePTY(context.Background())
		if !got.Supported || got.Available || got.Code != "pty_unavailable" || got.Qualification.Status != "pending" {
			t.Fatalf("%+v", got)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPTYObservationUnavailable$")
	cmd.Env = []string{helper + "=1"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated descriptor exhaustion: %v\n%s", err, output)
	}
}
