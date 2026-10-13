//go:build !windows

package testfixture_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestScriptInPlacePreservesOriginalUnixWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original-script")
	if err := os.WriteFile(path, []byte("originalA"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.UpdateScriptInPlace(t, path, []byte("originalB"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "originalB" {
		t.Fatal("original existing bytes changed", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("original existing-file mode changed", err)
	}
	created := filepath.Join(filepath.Dir(path), "created")
	if err := testfixture.UpdateScriptInPlace(t, created, []byte("createdC"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(created)
	if err != nil || string(data) != "createdC" {
		t.Fatal("original creation bytes changed", err)
	}
	invalid := filepath.Join(path, "invalid-child")
	originalErr := os.WriteFile(invalid, nil, 0700)
	actualErr := testfixture.UpdateScriptInPlace(t, invalid, nil, 0700)
	var originalPath, actualPath *os.PathError
	if !errors.As(originalErr, &originalPath) || !errors.As(actualErr, &actualPath) || !errors.Is(actualPath.Err, originalPath.Err) {
		t.Fatal("original invalid-parent error changed", originalErr, actualErr)
	}
}
