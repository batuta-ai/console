package testfixture

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Capture the actual installed Git before tests replace PATH with CLI fixtures.
var nativeFixtureGit, nativeFixtureGitErr = exec.LookPath("git")

func Argv(t testing.TB, original []string) []string {
	t.Helper()
	if len(original) == 0 || original[0] != "/bin/sh" {
		return original
	}
	if nativeFixtureGitErr != nil {
		t.Fatal(nativeFixtureGitErr)
	}
	shell := filepath.Join(filepath.Dir(filepath.Dir(nativeFixtureGit)), "usr", "bin", "sh.exe")
	data, err := os.ReadFile(shell)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual fixture shell %s sha256=%x", shell, sha256.Sum256(data))
	return append([]string{shell}, original[1:]...)
}

func TerminalEnvironment(t testing.TB, original []string) []string {
	t.Helper()
	temporary := PrivateHome(t)
	return append(append([]string(nil), original...), "TEMP="+temporary, "TMP="+temporary)
}
