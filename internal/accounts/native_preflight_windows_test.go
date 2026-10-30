package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/core/integration"
	"github.com/batuta-ai/core/publication"
)

// Observe only the real worktree-add result; ExecRunner retains command and
// context semantics, including waiting for the actual Git process to exit.
type nativePreflightObserver struct {
	target string
	stderr string
	exit   int
}

func (r *nativePreflightObserver) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	result, err := (publication.ExecRunner{}).Run(ctx, command)
	if len(command.Args) >= 4 && command.Args[0] == "worktree" && command.Args[1] == "add" && command.Args[2] == "--detach" {
		r.target = command.Args[3]
		r.exit = result.ExitCode
		stderr := result.Stderr
		if len(stderr) > 4096 {
			stderr = stderr[:4096]
		}
		r.stderr = string(stderr)
	}
	return result, err
}

func nativePreflightDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func nativePreflightCache(t *testing.T) (string, string) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	cache, err = filepath.EvalSymlinks(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cache {
		if c < 32 || c > 126 {
			t.Fatalf("selected fixture requires ASCII current cache: %q", cache)
		}
	}
	qa := filepath.Join(cache, "Batuta", "qa")
	// NewPrivateTemp adds a separator, the two-character prefix and 32 hex
	// digits. Reserve another separator and one padding component.
	padding := 116 - (len(qa) + 1 + 2 + 32 + 1)
	if padding < 1 {
		t.Fatalf("current cache cannot safely fit the 116-character fixture: %q", cache)
	}
	if err := consoleruntime.EnsurePrivateDirectory(qa); err != nil {
		t.Fatal(err)
	}
	qa, err = filepath.EvalSymlinks(qa)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := consoleruntime.NewPrivateTemp(qa, "p-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(owned); err != nil {
			t.Error(err)
		}
	})
	selected := filepath.Join(owned, strings.Repeat("d", padding))
	if err := consoleruntime.EnsurePrivateDirectory(selected); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(selected)
	if err != nil || canonical != selected || len(selected) != 116 {
		t.Fatalf("cannot establish canonical 116-character cache: %q canonical=%q err=%v", selected, canonical, err)
	}
	task43AccountACL(t, selected)
	return selected, owned
}

func TestNativeAnonymousCorePreflightDeepCache(t *testing.T) {
	selected, owned := nativePreflightCache(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("real Git is required:", err)
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", selected)
	env, home, err := SessionEnvironment()
	if err != nil {
		t.Fatal("fresh SessionEnvironment:", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	relative, err := filepath.Rel(selected, home)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, `..\`) || filepath.IsAbs(relative) {
		t.Fatalf("anonymous HOME escaped selected cache: %q err=%v", home, err)
	}
	identity := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("invalid session entry %q", entry)
		}
		identity[key] = value
		if key == "HOMEDRIVE" || key == "HOMEPATH" {
			continue
		}
		rel, err := filepath.Rel(home, value)
		if err != nil || rel == ".." || strings.HasPrefix(rel, `..\`) || filepath.IsAbs(rel) {
			t.Fatalf("%s escaped HOME: %q", key, value)
		}
		canonical, err := filepath.EvalSymlinks(value)
		if err != nil || canonical != value {
			t.Fatalf("noncanonical %s=%q: %v", key, value, err)
		}
		dir, err := consoleruntime.OpenPrivateDirectory(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := dir.Close(); err != nil {
			t.Fatal(err)
		}
		task43AccountACL(t, value)
	}
	if identity["HOME"] != home || identity["LOCALAPPDATA"] == "" {
		t.Fatal("incomplete anonymous identity")
	}
	if err := ValidateWindowsTemporaryEnvironment(env); err != nil {
		t.Fatal(err)
	}
	checkCredentials := func() {
		t.Helper()
		for _, name := range []string{filepath.Join(".codex", "auth.json"), filepath.Join(".claude", ".credentials.json")} {
			if _, err := os.Stat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("anonymous credentials %q: %v", name, err)
			}
		}
	}
	checkCredentials()
	// ExecRunner inherits os.Environ. Close inheritance here, with the same
	// production environment filter, so no provider secrets or Git overrides enter.
	runtimeEnv := RuntimeEnvironment(os.Environ(), env)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, entry := range runtimeEnv {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	globalConfig := filepath.Join(owned, "empty.gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_AUTHOR_NAME", "Native QA")
	t.Setenv("GIT_AUTHOR_EMAIL", "native-qa@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Native QA")
	t.Setenv("GIT_COMMITTER_EMAIL", "native-qa@example.invalid")
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := func(root string, args ...string) string {
		t.Helper()
		result, err := (publication.ExecRunner{}).Run(ctx, publication.Command{Executable: git, Directory: root, Args: args, StdoutLimit: integration.GitStdoutLimit, StderrLimit: integration.GitStderrLimit})
		if err != nil || result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated {
			t.Fatalf("git %q: exit=%d err=%v stderr=%q", args, result.ExitCode, err, result.Stderr)
		}
		return strings.TrimSpace(string(result.Stdout))
	}
	repo := filepath.Join(owned, "repo")
	if err := consoleruntime.EnsurePrivateDirectory(repo); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "--template=", "--initial-branch=main")
	run(repo, "config", "core.longpaths", "true")
	run(repo, "config", "commit.gpgsign", "false")
	write := func(root, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "product.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(repo, "base\n")
	run(repo, "add", "product.txt")
	run(repo, "commit", "-m", "chore: establish base")
	base := run(repo, "rev-parse", "HEAD")
	baseTree := run(repo, "rev-parse", "HEAD^{tree}")
	candidate := filepath.Join(owned, "candidate")
	branch := "batuta/task/task_01"
	run(repo, "worktree", "add", "-b", branch, candidate, base)
	candidate, err = filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatal(err)
	}
	write(candidate, "candidate\n")
	run(candidate, "add", "product.txt")
	run(candidate, "commit", "-m", "feat: implement task_01")
	commit := run(candidate, "rev-parse", "HEAD")
	tree := run(candidate, "rev-parse", "HEAD^{tree}")
	worktrees := run(repo, "worktree", "list", "--porcelain")
	verification := []byte(`{"status":"passed","task_id":"task_01"}`)
	observer := &nativePreflightObserver{}
	client := integration.GitClient{Executable: git, Runner: observer}
	evidence, err := client.Candidate(ctx, integration.CandidateRequest{TaskID: "task_01", Slug: "native", WorktreeRoot: candidate, RepositoryRoot: repo, ExpectedBranch: branch, BaseSHA: base, Verification: verification, VerificationDigest: nativePreflightDigest(verification)})
	if err != nil {
		t.Fatal("actual Candidate:", err)
	}
	if evidence.CommitSHA != commit || evidence.TreeSHA != tree || evidence.BaseSHA != base || evidence.RepositoryIdentity == "" || evidence.VerificationDigest != nativePreflightDigest(verification) {
		t.Fatalf("candidate evidence: %#v", evidence)
	}
	request := integration.PreflightRequest{OperationID: nativePreflightDigest([]byte("native-op")), RequestDigest: nativePreflightDigest([]byte("native-request")), IntegrationRoot: repo, StartingHeadSHA: base, Candidates: []integration.CandidateEvidence{evidence}}
	result, preflightErr := client.Preflight(ctx, request)
	// Check cleanup and original state even on RED, before reporting Preflight.
	for _, state := range []struct{ root, head, tree, branch, content string }{
		{repo, base, baseTree, "main", "base\n"}, {candidate, commit, tree, branch, "candidate\n"},
	} {
		if got := run(state.root, "rev-parse", "HEAD"); got != state.head {
			t.Errorf("HEAD changed: %q", got)
		}
		if got := run(state.root, "rev-parse", "HEAD^{tree}"); got != state.tree {
			t.Errorf("tree changed: %q", got)
		}
		if got := run(state.root, "symbolic-ref", "--short", "HEAD"); got != state.branch {
			t.Errorf("branch changed: %q", got)
		}
		if got := run(state.root, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
			t.Errorf("dirty worktree: %q", got)
		}
		data, err := os.ReadFile(filepath.Join(state.root, "product.txt"))
		if err != nil || string(data) != state.content {
			t.Errorf("worktree content changed: %q err=%v", data, err)
		}
	}
	if got := run(repo, "worktree", "list", "--porcelain"); got != worktrees {
		t.Errorf("worktree registrations changed: %q", got)
	}
	if observer.target == "" {
		t.Error("actual disposable worktree add was not reached")
	} else {
		if _, err := os.Stat(filepath.Dir(observer.target)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("operation scratch survived: %v", err)
		}
		entries, err := os.ReadDir(filepath.Dir(filepath.Dir(observer.target)))
		if err != nil || len(entries) != 0 {
			t.Errorf("scratch not empty: %v err=%v", entries, err)
		}
	}
	checkCredentials()
	if preflightErr != nil {
		t.Fatalf("actual Preflight: %v; worktree-add target=%q exit=%d bounded stderr=%q", preflightErr, observer.target, observer.exit, observer.stderr)
	}
	if result.OperationID != request.OperationID || result.RequestDigest != request.RequestDigest || result.StartingHeadSHA != base || !reflect.DeepEqual(result.AcceptedTaskIDs, []string{"task_01"}) || !reflect.DeepEqual(result.AcceptedCommitSHAs, []string{commit}) || !reflect.DeepEqual(result.AcceptedResultTreeSHAs, []string{tree}) || len(result.AcceptedResultCommitSHAs) != 1 || result.ResultingHeadSHA != result.AcceptedResultCommitSHAs[0] || result.ResultingHeadSHA == base || result.FirstConflictTaskID != "" || result.ConflictEvidenceDigest != "" {
		t.Fatalf("actual preflight result: %#v", result)
	}
}
