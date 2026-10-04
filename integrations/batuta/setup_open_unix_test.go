//go:build darwin || linux

package batuta

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSetupFileSafeOpen(t *testing.T) {
	for _, kind := range []string{"regular", "limit", "oversize", "directory", "fifo", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "WORK.md")
			payload := []byte("caller content")
			var err error
			switch kind {
			case "limit", "oversize":
				size := SetupFileLimit
				if kind == "oversize" {
					size++
				}
				payload = bytes.Repeat([]byte("x"), size)
				err = os.WriteFile(path, payload, 0600)
			case "regular":
				err = os.WriteFile(path, payload, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "symlink":
				if err := os.WriteFile(filepath.Join(dir, "target"), payload, 0600); err != nil {
					t.Fatal(err)
				}
				// A target inside Root must still be rejected by the final open.
				err = os.Symlink("target", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			got, err := readSetupFile(root, "WORK.md")
			if kind == "regular" || kind == "limit" {
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("regular bounded file: bytes=%d error=%v", len(got), err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("unsafe file accepted: bytes=%d error=%v", len(got), err)
			}
		})
	}
}
