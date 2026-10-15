package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
)

// This exercises the unselected command boundary, not the full Core helper or
// a replay of the original review snapshot. No source-tree fixture is needed.
func TestNativeCoreShellGitProofLongPath(t *testing.T) {
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	nativeManager := session.New(home)
	t.Cleanup(func() {
		if err := nativeManager.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	nativeShell, err := nativeCoreShellPath()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Dir(nativeShell) + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)
	// Match RunNativeCore's process-local inputs; NativeEnvironment decides
	// which Git settings actually reach each unselected command.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.longpaths")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	env = append(env, "PATH="+path)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Bool
	commands := nativeCoreCommands{other: session.NativeRunner{Manager: nativeManager, Environment: env}, active: &active}
	recording := &nativeGitProofRecorder{t: t, commands: commands}
	shell, err := (nativeCoreReviewRunner{commands: commands}).shell(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	shell.Runner = recording
	t.Logf("nativeShell=%q reviewShell=%q resolvedGit=%q ownedHome=%q ambientGit=[GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.longpaths GIT_CONFIG_VALUE_0=true]", nativeShell, shell.Shell, git, home)

	root := filepath.Join(home, "review-snapshot")
	relative := filepath.Join(strings.Repeat("tracked-", 7), strings.Repeat("nested-", 8), strings.Repeat("nested-", 8), strings.Repeat("nested-", 8), strings.Repeat("nested-", 8), "proof.txt")
	tracked := filepath.Join(root, relative)
	t.Logf("ownedCwd=%q cwdUTF16=%d trackedRelative=%q relativeUTF16=%d trackedAbsolute=%q absoluteUTF16=%d", root, len(utf16.Encode([]rune(root))), relative, len(utf16.Encode([]rune(relative))), tracked, len(utf16.Encode([]rune(tracked))))
	if len(utf16.Encode([]rune(filepath.Join(root, ".git", "index.lock")))) > 258 {
		t.Fatal("owned repository root is too long for the short-path control; use a shorter native LOCALAPPDATA parent")
	}
	if len(utf16.Encode([]rune(relative))) <= 260 || len(utf16.Encode([]rune(tracked))) <= 260 {
		t.Fatal("fixture must exceed legacy MAX_PATH")
	}
	if err := os.MkdirAll(filepath.Dir(tracked), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	direct := func(args ...string) (publication.CommandResult, error) {
		return recording.Run(ctx, publication.Command{Executable: git, Args: args, Directory: root, Environment: []string{"CI=1", "BATUTA=1"}, StdoutLimit: 4 << 20, StderrLimit: 1 << 20})
	}
	for _, args := range [][]string{{"init", "--template="}, {"add", "--", relative}} {
		if result, err := direct(args...); err != nil || result.ExitCode != 0 {
			t.Fatalf("real Git fixture setup %q: exit=%d error=%v", args, result.ExitCode, err)
		}
	}
	if err := os.WriteFile(tracked, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := direct("diff", "--name-only"); err != nil || result.ExitCode != 0 || strings.TrimSpace(string(result.Stdout)) != filepath.ToSlash(relative) {
		t.Fatalf("long tracked path must have an unstaged diff: exit=%d error=%v stdout=%q", result.ExitCode, err, result.Stdout)
	}
	if code, output, err := shell.Run(ctx, root, "command -v git && git --version"); err != nil || code != 0 {
		t.Errorf("shell Git resolution: exit=%d error=%v output=%q", code, err, output)
	}
	// An unset core.longpaths returns 1; retain that distinction from a launch
	// failure without imposing a value or changing any Git configuration.
	if code, output, err := shell.Run(ctx, root, "git config --show-origin --get-all core.longpaths"); err != nil || code != 0 && code != 1 {
		t.Errorf("shell Git configuration: exit=%d error=%v output=%q", code, err, output)
	}
	if result, err := direct("diff", "--check"); err != nil || result.ExitCode != 0 {
		t.Errorf("direct Git long-path control: exit=%d error=%v", result.ExitCode, err)
	}
	rules := []review.SpecRule{{ID: "long-path-proof", Text: "Long tracked paths support Git whitespace proof", Proof: "git diff --check"}}
	results, remaining, err := review.RunSpecProofs(ctx, root, rules, shell)
	t.Logf("Core55 RunSpecProofs results=%+v remaining=%+v error=%v", results, remaining, err)
	if err != nil || len(remaining) != 0 || len(results) != 1 || results[0].Status != review.CriterionSatisfied || recording.result.ExitCode != 0 || recording.err != nil {
		t.Fatalf("shell Git long-path proof must pass: result=%+v error=%v nativeExit=%d nativeError=%v", results, err, recording.result.ExitCode, recording.err)
	}
}

type nativeGitProofRecorder struct {
	t        *testing.T
	commands nativeCoreCommands
	result   publication.CommandResult
	err      error
}

func (r *nativeGitProofRecorder) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	r.t.Helper()
	gitKeys := func(env []string) []string {
		var entries []string
		for _, entry := range env {
			if strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				entries = append(entries, entry)
			}
		}
		return entries
	}
	effective, envErr := session.NativeEnvironment(r.commands.other.Environment, command.Environment)
	r.t.Logf("native Git boundary executable=%q args=%q cwd=%q commandGit=%q explicitGit=%q effectiveGit=%q environmentError=%v stdoutLimit=%d stderrLimit=%d", command.Executable, command.Args, command.Directory, gitKeys(command.Environment), gitKeys(r.commands.other.Environment), gitKeys(effective), envErr, command.StdoutLimit, command.StderrLimit)
	r.result, r.err = r.commands.Run(ctx, command)
	r.t.Logf("native Git result exit=%d error=%v stdoutTruncated=%t stderrTruncated=%t stdout=%q stderr=%q", r.result.ExitCode, r.err, r.result.StdoutTruncated, r.result.StderrTruncated, r.result.Stdout, r.result.Stderr)
	return r.result, r.err
}
