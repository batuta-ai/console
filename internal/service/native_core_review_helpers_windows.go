/*
Core49 review CLI orchestration adapted from github.com/batuta-ai/core v1.1.0-beta.49
cmd/batuta/main.go. Public review/executor APIs provide the review engine.

# MIT License

# Copyright (c) 2026 Francisross Soares de Oliveira

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
)

// nativeCoreReviewGuardPaths lists every file the review may create or prune: the
// report artifacts, the state file and the cohort tails already on disk.
func nativeCoreReviewGuardPaths(directory string, report review.Report, statePath string) []string {
	paths := append(review.ArtifactPaths(directory, report), statePath)
	return append(paths, review.ExistingTailPaths(directory)...)
}

func nativeCoreReviewSessionError(ctx context.Context, git publication.GitClient, root string, baseline publication.WorktreeState, trackedBaseline map[string]string, sessionErr error) error {
	after, stateErr := git.WorktreeState(ctx, root)
	if stateErr != nil {
		return errors.Join(sessionErr, fmt.Errorf("review: verify source tree: %w", stateErr))
	}
	if after == baseline {
		return sessionErr
	}
	paths := nativeCoreChangedTrackedPaths(root, baseline.HeadSHA, trackedBaseline)
	if len(paths) == 0 {
		paths = []string{"worktree state changed"}
	}
	return errors.Join(sessionErr, fmt.Errorf("review: source tree changed during review: %s", strings.Join(paths, ", ")))
}

// Exclude only the authorized artifact files from the publication tree guard;
// other tracked, staged and untracked changes still invalidate the review.
type nativeCoreReviewPublicationRunner struct {
	paths  []string
	runner publication.CommandRunner
}

func (r nativeCoreReviewPublicationRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	if len(command.Args) > 0 {
		switch command.Args[0] {
		case "status", "diff", "ls-files":
			command.Args = append(command.Args, "--", ".")
			for _, filename := range r.paths {
				relative, err := filepath.Rel(command.Directory, filename)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					command.Args = append(command.Args, ":(top,literal,exclude)"+filepath.ToSlash(relative))
				}
			}
		}
	}
	return r.runner.Run(ctx, command)
}

func nativeCoreReviewSlug(root, spec string) (string, error) {
	if spec != "" {
		name := strings.TrimSuffix(filepath.Base(spec), filepath.Ext(spec))
		name = strings.TrimPrefix(name, "plan-")
		return name, nil
	}
	output, err := nativeCoreReviewGitCommand(root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return "detached-head", nil
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(string(output))) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "branch"
	}
	return slug, nil
}

func nativeCoreReviewBranchIdentity(root string) (string, error) {
	output, err := nativeCoreReviewGitCommand(root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	output, err = nativeCoreReviewGitCommand(root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("review: determine branch identity: %w", err)
	}
	return "detached-head:" + strings.TrimSpace(string(output)), nil
}

func nativeCoreReviewSpecPath(root, spec string) (string, error) {
	paths := []string{spec}
	if !filepath.IsAbs(spec) && !strings.ContainsAny(spec, "/\\") && filepath.Ext(spec) == "" {
		paths = []string{routing.PlanPath(spec), filepath.Join(".batuta", "plans", "done", spec+".md"), filepath.Join(".batuta", "plan-"+spec+".md")}
	}
	for _, filename := range paths {
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(root, filename)
		}
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("review: resolve spec path: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(filename)
		if err != nil {
			return "", fmt.Errorf("review: resolve spec path: %w", err)
		}
		return filepath.Clean(resolved), nil
	}
	return "", fmt.Errorf("review: plan %q is unavailable", spec)
}

func nativeCoreReviewStateKey(branchSlug, branchIdentity, specSlug, resolvedSpecPath string) string {
	name := branchSlug
	if specSlug != "" {
		name += "-" + specSlug
	}
	digest := sha256.Sum256([]byte(branchIdentity + "\x00" + resolvedSpecPath))
	return fmt.Sprintf("%s-%x", name, digest[:8])
}

func nativeCoreDefaultReviewBase(root string) (string, error) {
	for _, candidate := range []string{"main", "origin/main", "master", "origin/master", "@{upstream}", "HEAD^", "HEAD"} {
		cmd := nativeCoreReviewGitCommand(root, "merge-base", "HEAD", candidate)
		if output, err := cmd.Output(); err == nil && strings.TrimSpace(string(output)) != "" {
			return strings.TrimSpace(string(output)), nil
		}
	}
	return "", errors.New("review: cannot determine a default base; pass --base <ref>")
}

func nativeCoreTrackedChangeSignatures(root, beforeHead string) (map[string]string, error) {
	output, err := nativeCoreReviewGitCommand(root, "diff", "--name-only", "-z", beforeHead, "--").Output()
	if err != nil {
		return nil, fmt.Errorf("review: list tracked changes: %w", err)
	}
	signatures := make(map[string]string)
	for _, name := range bytes.Split(output, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		diff, err := nativeCoreReviewGitCommand(root, "diff", "--binary", "--no-ext-diff", beforeHead, "--", string(name)).Output()
		if err != nil {
			return nil, fmt.Errorf("review: inspect tracked change %q: %w", name, err)
		}
		signatures[string(name)] = string(diff)
	}
	return signatures, nil
}

func nativeCoreChangedTrackedPaths(root, beforeHead string, baseline map[string]string) []string {
	after, err := nativeCoreTrackedChangeSignatures(root, beforeHead)
	if err != nil {
		return nil
	}
	var paths []string
	for name, signature := range after {
		if baseline[name] != signature {
			paths = append(paths, name)
		}
	}
	for name := range baseline {
		if _, exists := after[name]; !exists {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	return paths
}

func nativeCoreReviewGitCommand(root string, args ...string) *nativeCoreGitOutput {
	return &nativeCoreGitOutput{command: exec.Command("git", append([]string{"-C", root}, args...)...)}
}

type nativeCoreGitOutput struct{ command *exec.Cmd }

func (c *nativeCoreGitOutput) Output() ([]byte, error) {
	output, err := c.command.Output()
	var failure *exec.ExitError
	if errors.As(err, &failure) {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(failure.Stderr)))
	}
	return output, err
}
