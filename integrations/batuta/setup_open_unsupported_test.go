//go:build !darwin && !linux

package batuta

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupFileUnsupportedPlatform(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".batuta"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".batuta", "profile.md"), []byte("Stack: Go\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if file, err := openSetupFile(root, ".batuta/profile.md"); file != nil || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("unsafe fallback: file=%v error=%v", file, err)
	}
	if got := ReadSetup(dir); got.Ready || got.Code != "setup_unsupported_platform" {
		t.Fatalf("unsupported reader diagnostic: %+v", got)
	}
}
