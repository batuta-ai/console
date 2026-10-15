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
	shell, err := (nativeCoreReviewRunner{commands: commands}).shell(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nativeShell=%q reviewShell=%q resolvedGit=%q ownedHome=%q ambientGit=[GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.longpaths GIT_CONFIG_VALUE_0=true]", nativeShell, shell.Shell, git, home)

	ctx := context.Background()
	for _, fixture := range []struct {
		name      string
		relative  string
		rootUTF16 int
	}{
		{"long-tracked-path", filepath.Join(strings.Repeat("tracked-", 7), strings.Repeat("nested-", 8), strings.Repeat("nested-", 8), strings.Repeat("nested-", 8), strings.Repeat("nested-", 8), "proof.txt"), 0},
		{"review-root-depth", "out.txt", 220},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := filepath.Join(home, fixture.name)
			if fixture.rootUTF16 != 0 {
				padding := fixture.rootUTF16 - len(utf16.Encode([]rune(root))) - 1
				if padding < 1 || padding > 255 {
					t.Fatalf("owned root %q cannot fit a %d UTF16 repository root: padding=%d", root, fixture.rootUTF16, padding)
				}
				root = filepath.Join(root, strings.Repeat("r", padding))
			}
			relative := fixture.relative
			tracked := filepath.Join(root, relative)
			t.Logf("ownedCwd=%q cwdUTF16=%d trackedRelative=%q relativeUTF16=%d trackedAbsolute=%q absoluteUTF16=%d", root, len(utf16.Encode([]rune(root))), relative, len(utf16.Encode([]rune(relative))), tracked, len(utf16.Encode([]rune(tracked))))
			if len(utf16.Encode([]rune(filepath.Join(root, ".git", "index.lock")))) > 258 {
				t.Fatal("owned repository root is too long for the short-path control; use a shorter native LOCALAPPDATA parent")
			}
			if fixture.rootUTF16 == 0 && (len(utf16.Encode([]rune(relative))) <= 260 || len(utf16.Encode([]rune(tracked))) <= 260) {
				t.Fatal("fixture must exceed legacy MAX_PATH")
			}
			if fixture.rootUTF16 != 0 && len(utf16.Encode([]rune(root))) != fixture.rootUTF16 {
				t.Fatal("fixture must match the review repository root depth")
			}
			if err := os.MkdirAll(filepath.Dir(tracked), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tracked, []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			recording := &nativeGitProofRecorder{t: t, commands: commands}
			shell := shell
			shell.Runner = recording
			direct := func(args ...string) (publication.CommandResult, error) {
				return recording.Run(ctx, publication.Command{Executable: git, Args: args, Directory: root, Environment: []string{"CI=1", "BATUTA=1"}, StdoutLimit: 4 << 20, StderrLimit: 1 << 20})
			}
			for _, args := range [][]string{{"init", "--template="}, {"add", "--", relative}} {
				if result, err := direct(args...); err != nil || result.ExitCode != 0 {
					t.Fatalf("real Git fixture setup %q: exit=%d error=%v", args, result.ExitCode, err)
				}
			}
			if code, output, err := shell.Run(ctx, root, "command -v git && git --version"); err != nil || code != 0 {
				t.Errorf("shell Git resolution: exit=%d error=%v output=%q", code, err, output)
			}
			// An unset core.longpaths returns 1; retain that distinction from a launch
			// failure without imposing a value or changing any Git configuration.
			if code, output, err := shell.Run(ctx, root, "git config --show-origin --get-all core.longpaths"); err != nil || code != 0 && code != 1 {
				t.Errorf("shell Git configuration: exit=%d error=%v output=%q", code, err, output)
			}
			for _, proof := range []struct {
				name    string
				content string
				exit    int
				status  review.CriterionStatus
			}{
				{"valid", "after\n", 0, review.CriterionSatisfied},
				{"trailing-whitespace", "after \n", 2, review.CriterionViolated},
			} {
				t.Run(proof.name, func(t *testing.T) {
					recording.t = t
					if err := os.WriteFile(tracked, []byte(proof.content), 0600); err != nil {
						t.Fatal(err)
					}
					// Exit zero alone can hide a skipped long file. Require its actual
					// changed content before checking the proof's whitespace verdict.
					diff, diffErr := direct("diff", "--no-color", "--no-ext-diff", "--no-textconv")
					_, _, shellDiffErr := shell.Run(ctx, root, "git diff --no-color --no-ext-diff --no-textconv")
					for _, got := range []struct {
						name   string
						result publication.CommandResult
						err    error
					}{{"direct", diff, diffErr}, {"shell", recording.result, shellDiffErr}} {
						output := strings.ReplaceAll(string(got.result.Stdout), "\r\n", "\n")
						if got.err != nil || got.result.ExitCode != 0 || !strings.Contains(output, filepath.ToSlash(relative)) || !strings.Contains(output, "-before\n") || !strings.Contains(output, "+"+proof.content) || strings.Contains(strings.ToLower(string(got.result.Stderr)), "filename too long") || got.result.StdoutTruncated || got.result.StderrTruncated {
							t.Errorf("%s Git must read the tracked content: exit=%d error=%v stdout=%q stderr=%q", got.name, got.result.ExitCode, got.err, got.result.Stdout, got.result.Stderr)
						}
					}
					directProof, directErr := direct("diff", "--check")
					rules := []review.SpecRule{{ID: "long-path-proof", Text: "Tracked file whitespace is valid", Proof: "git diff --check"}}
					results, remaining, err := review.RunSpecProofs(ctx, root, rules, shell)
					t.Logf("Core55 RunSpecProofs results=%+v remaining=%+v error=%v", results, remaining, err)
					if err != nil || len(remaining) != 0 || len(results) != 1 || results[0].Status != proof.status {
						t.Errorf("shell Git whitespace proof must be %s: results=%+v remaining=%+v error=%v", proof.status, results, remaining, err)
					}
					for _, got := range []struct {
						name   string
						result publication.CommandResult
						err    error
					}{{"direct", directProof, directErr}, {"shell", recording.result, recording.err}} {
						if got.err != nil || got.result.ExitCode != proof.exit || strings.Contains(strings.ToLower(string(got.result.Stderr)), "filename too long") || got.result.StdoutTruncated || got.result.StderrTruncated {
							t.Errorf("%s Git whitespace proof: want exit=%d got exit=%d error=%v stdout=%q stderr=%q", got.name, proof.exit, got.result.ExitCode, got.err, got.result.Stdout, got.result.Stderr)
						}
						output := strings.ReplaceAll(string(got.result.Stdout), "\r\n", "\n")
						if proof.exit != 0 && (!strings.Contains(output, filepath.ToSlash(relative)+":1: trailing whitespace.") || !strings.Contains(output, "+"+proof.content)) {
							t.Errorf("%s Git must report this file's invalid whitespace: stdout=%q stderr=%q", got.name, got.result.Stdout, got.result.Stderr)
						}
					}
				})
			}
		})
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
