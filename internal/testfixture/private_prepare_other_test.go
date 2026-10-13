//go:build !windows

package testfixture_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestPrivateCredentialPreparationPreservesOriginalNonWindowsWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := testfixture.PreparePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preparation changed original Unix creation", err)
	}
	want := "synthetic-only"
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.PreparePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatal("original Unix credential bytes changed", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("original Unix credential mode changed", err)
	}
}
