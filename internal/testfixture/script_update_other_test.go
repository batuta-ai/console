//go:build !windows

package testfixture_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestScriptUpdatePreservesOriginalUnixOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original-script")
	if err := os.WriteFile(path, []byte("originalA"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.RewriteScript(t, path, []byte("originalB"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "originalB" {
		t.Fatal("original rewrite bytes changed", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("original existing file mode semantics changed", err)
	}
	if err := testfixture.RemoveScript(t, path); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.RemoveScript(t, path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original missing removal error changed", err)
	}
	if err := testfixture.RewriteScript(t, path, []byte("newC"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "newC" {
		t.Fatal("original creation bytes changed", err)
	}
}
