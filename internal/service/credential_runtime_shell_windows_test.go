package service

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func credentialRuntimeShell(t *testing.T) string {
	t.Helper()
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(directory, "cmd.exe")
}
