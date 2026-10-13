package testfixture

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Capture the actual installed Git before tests replace PATH with CLI fixtures.
var nativeFixtureGit, nativeFixtureGitErr = exec.LookPath("git")

func Argv(t testing.TB, original []string) []string {
	t.Helper()
	if len(original) == 0 || (original[0] != "/bin/sh" && original[0] != "/bin/cat") {
		return original
	}
	if nativeFixtureGitErr != nil {
		t.Fatal(nativeFixtureGitErr)
	}
	shell := filepath.Join(filepath.Dir(filepath.Dir(nativeFixtureGit)), "usr", "bin", "sh.exe")
	kind := "shell"
	if original[0] == "/bin/cat" {
		shell = filepath.Join(filepath.Dir(shell), "cat.exe")
		kind = "cat"
	}
	data, err := os.ReadFile(shell)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual fixture %s %s sha256=%x", kind, shell, sha256.Sum256(data))
	return append([]string{shell}, original[1:]...)
}

func TerminalEnvironment(t testing.TB, original []string) []string {
	t.Helper()
	if nativeFixtureGitErr != nil {
		t.Fatal(nativeFixtureGitErr)
	}
	runtimePath := os.Getenv("PATH")
	for _, entry := range original {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			runtimePath = value
		}
	}
	utilities := filepath.Join(filepath.Dir(filepath.Dir(nativeFixtureGit)), "usr", "bin")
	temporary := PrivateHome(t)
	return append(append([]string(nil), original...), "TEMP="+temporary, "TMP="+temporary, "PATH="+utilities+string(os.PathListSeparator)+runtimePath)
}
