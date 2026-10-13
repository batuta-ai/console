//go:build !windows

package testfixture_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestScriptWriterPreservesOriginalNonWindowsFileAndExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actual script")
	body := []byte("#!/bin/sh\nprintf '%s' \"$1\"\nexit 44\n")
	if err := testfixture.WriteScript(t, path, body, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, body) {
		t.Fatal("ordinary script bytes changed", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("ordinary script permissions changed", info, err)
	}
	literal := "space '\" $(literal);* ação\nsecond line"
	output, err := exec.Command(path, literal).Output()
	if failure, ok := err.(*exec.ExitError); !ok || failure.ExitCode() != 44 || string(output) != literal {
		t.Fatal("ordinary script execution changed", string(output), err)
	}
}
