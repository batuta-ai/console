//go:build !windows

package limits

import (
	"os"
	"runtime"
	"syscall"
	"testing"
)

func makeImportFIFO(t *testing.T, path string) error { return syscall.Mkfifo(path, 0o600) }

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	path := "/dev/fd"
	if runtime.GOOS == "linux" {
		path = "/proc/self/fd"
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
