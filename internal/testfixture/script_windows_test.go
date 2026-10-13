package testfixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
)

func scriptNativeRunner(t *testing.T, root string) (session.NativeRunner, []string) {
	t.Helper()
	manager := session.New(t.TempDir())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	environment := testfixture.TerminalEnvironment(t, []string{"HOME=" + root, "PATH=" + root, "LANG=C.UTF-8"})
	return session.NativeRunner{Manager: manager, Environment: environment}, environment
}

func TestNativeScriptLiteralArgumentsAndExit(t *testing.T) {
	root := testfixture.PrivateHome(t)
	executable := filepath.Join(root, "actual CLI ação.exe")
	script := []byte("#!/bin/sh\nprintf '%s\\0' \"$0\" \"$@\"\nIFS= read -r input\nprintf '%s\\0' \"$input\" \"$HOME\"\nprintf '%s' 'actual-script-stderr' >&2\nexit 44\n")
	if err := testfixture.WriteScript(t, executable, script, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"space value", "quote'\"", "$(literal);*", "ação 日本語", "line1\nline2", "/tmp/path-looking", "C:\\literal\\path", "\\\\server\\share", "--version"}
	runner, _ := scriptNativeRunner(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, publication.Command{Executable: executable, Args: args, Directory: root, Stdin: []byte("finite input\n")})
	if err != nil || result.ExitCode != 44 {
		t.Fatalf("actual script execution exit=%d error=%v stderr=%q", result.ExitCode, err, result.Stderr)
	}
	want := append(append([]string{executable}, args...), "finite input", root)
	got := strings.Split(string(result.Stdout), "\x00")
	if len(got) == 0 || got[len(got)-1] != "" {
		t.Fatalf("actual NUL-delimited stdout=%q", result.Stdout)
	}
	got = got[:len(got)-1]
	if !slices.Equal(got, want) || string(result.Stderr) != "actual-script-stderr" || result.StdoutTruncated || result.StderrTruncated {
		t.Fatalf("actual argv/stdin/home=%q want=%q stderr=%q truncated=%v/%v", got, want, result.Stderr, result.StdoutTruncated, result.StderrTruncated)
	}
}

type nativeScriptRecord struct {
	Args            []string
	Home, Path, CWD string
	Input           []byte
}

func TestNativeScriptRecordChild(t *testing.T) {
	if len(os.Args) < 4 || os.Args[3] != "console-script-record" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	record := nativeScriptRecord{Args: os.Args[4:], Home: os.Getenv("HOME"), Path: os.Getenv("PATH"), CWD: cwd, Input: input}
	if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stderr, "actual-record-stderr"); err != nil {
		t.Fatal(err)
	}
	os.Exit(44)
}

func TestNativeScriptPreservesActualChildEnvironment(t *testing.T) {
	root := testfixture.PrivateHome(t)
	executable := filepath.Join(root, "actual native recorder.exe")
	child, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := []byte("#!/bin/sh\nexec " + quote(child) + " -test.run=^TestNativeScriptRecordChild$ -- console-script-record \"$@\"\n")
	if err := testfixture.WriteScript(t, executable, script, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"/tmp/exact literal", "C:\\literal\\path", "quote'\"", "ação 日本語", "two\nlines"}
	input := []byte{0, 27, 'q', 26, 10}
	runner, environment := scriptNativeRunner(t, root)
	expectedPath := ""
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			expectedPath = value
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, publication.Command{Executable: executable, Args: args, Directory: root, Stdin: input})
	if err != nil || result.ExitCode != 44 {
		t.Fatalf("actual native record exit=%d error=%v stderr=%q", result.ExitCode, err, result.Stderr)
	}
	var record nativeScriptRecord
	if err := json.Unmarshal(result.Stdout, &record); err != nil {
		t.Fatalf("native record parse: %v; stdout=%q", err, result.Stdout)
	}
	if !slices.Equal(record.Args, args) || record.Home != root || record.Path != expectedPath || record.CWD != root || !bytes.Equal(record.Input, input) || string(result.Stderr) != "actual-record-stderr" {
		t.Fatalf("actual child record=%+v wantArgs=%q home/cwd=%q PATH=%q input=%x stderr=%q", record, args, root, expectedPath, input, result.Stderr)
	}
}

func TestNativeScriptRefusesExistingTarget(t *testing.T) {
	root := testfixture.PrivateHome(t)
	target := filepath.Join(root, "existing.exe")
	original := []byte("original-existing-object")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.WriteScript(t, target, []byte("#!/bin/sh\nexit 99\n"), 0700); err == nil {
		t.Fatal("native fixture adopted an existing target")
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("existing target changed: %x err=%v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing.exe" {
		t.Fatalf("refused target directory changed: %v err=%v", entries, err)
	}
}
