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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/gates"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
)

type nativeCoreReviewRunner struct {
	commands nativeCoreCommands
	skills   string
}
type nativeCoreReviewExit struct{ code int }

func (e *nativeCoreReviewExit) Error() string { return fmt.Sprintf("review verdict exit %d", e.code) }

func (r nativeCoreReviewRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	failed := publication.CommandResult{ExitCode: -1}
	if len(command.Args) == 0 || command.Executable != "console-native-core-review" || !filepath.IsAbs(command.Directory) {
		return failed, domain.ErrInvalidReference
	}
	if err := r.commands.rpc.call(ctx, "guard", &nativeCoreGuard{Cleanup: r.commands.rpc.master.Err() != nil && ctx.Err() == nil}); err != nil {
		return failed, err
	}
	if len(command.Args) == 1 && command.Args[0] == "capabilities" {
		hello, err := actualNativeCoreHello()
		if err != nil {
			return failed, err
		}
		data, err := json.Marshal(struct {
			Version  string   `json:"version"`
			Commands []string `json:"commands"`
		}{hello.CoreVersion, []string{"review"}})
		return publication.CommandResult{ExitCode: 0, Stdout: data}, err
	}
	if command.Args[0] != "review" {
		return failed, domain.ErrInvalidReference
	}
	var stdout, stderr bytes.Buffer
	err := r.run(ctx, command.Directory, command.Args[1:], &stdout, &stderr)
	result := publication.CommandResult{ExitCode: 0, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var verdict *nativeCoreReviewExit
	if errors.As(err, &verdict) {
		result.ExitCode = verdict.code
		err = nil
	} else if errors.Is(err, flag.ErrHelp) {
		result.ExitCode = 1
		err = nil
	} else if err != nil {
		result.ExitCode = -1
	}
	if errors.Is(err, session.ErrNativeCleanupUnresolved) {
		err = errors.Join(publication.ErrReviewCleanupUnresolved, err)
	}
	return result, err
}

func (r nativeCoreReviewRunner) options(root string, parallel int) review.SessionOptions {
	readonly := r.commands
	readonly.readonly = true
	return review.SessionOptions{Root: root, Skills: r.skills, Parallel: parallel, Subprocess: &executor.Subprocess{Runner: readonly, Environment: []string{"GIT_CONFIG_COUNT=0"}, Lookup: func(cli string) (string, error) {
		a, ok := readonly.selected.byCLI[cli]
		if !ok {
			return "", domain.ErrInvalidReference
		}
		return a.executable, nil
	}}, LintRunner: nativeCorePOSIXRunner{commands: readonly}}
}

func (r nativeCoreReviewRunner) shell(timeout time.Duration) (gates.ShellRunner, error) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		return gates.ShellRunner{}, err
	}
	return gates.ShellRunner{Runner: r.commands, Shell: shell, Timeout: timeout}, nil
}

type nativeCorePOSIXRunner struct{ commands nativeCoreCommands }

func (r nativeCorePOSIXRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	if command.Executable == "/bin/sh" {
		shell, err := exec.LookPath("sh")
		if err != nil {
			return publication.CommandResult{ExitCode: -1}, err
		}
		command.Executable = shell
	}
	return r.commands.Run(ctx, command)
}

func (r nativeCoreReviewRunner) run(ctx context.Context, root string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("base", "", "base git ref (default: the branch point or HEAD)")
	includeWorktree := flags.Bool("worktree", false, "include untracked, non-ignored files")
	spec := flags.String("spec", "", "plan slug or plan file whose criteria are the review spec")
	cohortFiles := flags.Int("cohort-files", review.CohortFiles, "maximum files per cohort")
	parallel := flags.Int("parallel", 1, "reviewer sessions to run in parallel")
	reviewer := flags.String("reviewer", "", "reviewer override as executor/model")
	full := flags.Bool("full", false, "ignore prior review state and review from --base")
	out := flags.String("out", "", "artifact directory (default: .batuta/reviews/<date>-<slug>)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("review accepts flags only")
	}
	if *cohortFiles < 1 || *parallel < 1 {
		return errors.New("review requires positive --cohort-files and --parallel values")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	git := publication.GitClient{Executable: gitPath, Runner: r.commands}
	var rules []review.SpecRule
	var resolvedSpecPath string
	if *spec != "" {
		rules, err = review.LoadSpecCriteria(root, *spec)
		if err != nil {
			return fmt.Errorf("review: load spec %s: %w", *spec, err)
		}
		resolvedSpecPath, err = nativeCoreReviewSpecPath(root, *spec)
		if err != nil {
			return err
		}
	}
	options := r.options(root, *parallel)
	if options.Timeout < 0 {
		return errors.New("review: timeout must not be negative")
	}
	if options.Timeout == 0 {
		options.Timeout = 10 * time.Minute
	}
	baseline, err := git.WorktreeState(ctx, root)
	if err != nil {
		return err
	}
	trackedBaseline, err := nativeCoreTrackedChangeSignatures(root, baseline.HeadSHA)
	if err != nil {
		return err
	}
	var proofResults []review.SpecResult
	var sweepRules []review.SpecRule
	if *spec != "" {
		shell, err := r.shell(options.Timeout)
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.Proof == "" {
				sweepRules = append(sweepRules, rule)
				continue
			}
			results, _, proofErr := review.RunSpecProofs(ctx, root, []review.SpecRule{rule}, shell)
			after, stateErr := git.WorktreeState(ctx, root)
			if stateErr != nil {
				return fmt.Errorf("review: verify source tree: %w", stateErr)
			}
			if after != baseline {
				paths := nativeCoreChangedTrackedPaths(root, baseline.HeadSHA, trackedBaseline)
				if len(paths) == 0 {
					paths = []string{"worktree state changed"}
				}
				return fmt.Errorf("review: proof of %s changed the source tree: %s", rule.ID, strings.Join(paths, ", "))
			}
			if proofErr != nil {
				return proofErr
			}
			proofResults = append(proofResults, results...)
		}
	}
	slug, err := nativeCoreReviewSlug(root, *spec)
	if err != nil {
		return err
	}
	branch, err := nativeCoreReviewSlug(root, "")
	if err != nil {
		return err
	}
	branchIdentity, err := nativeCoreReviewBranchIdentity(root)
	if err != nil {
		return err
	}
	specSlug := ""
	if *spec != "" {
		specSlug = slug
	}
	key := nativeCoreReviewStateKey(branch, branchIdentity, specSlug, resolvedSpecPath)
	statePath := filepath.Join(root, ".batuta", "reviews", "state", key+".json")
	sessionSlug := time.Now().Format("2006-01-02") + "-" + slug
	directory := *out
	if directory == "" {
		directory = filepath.Join(root, ".batuta", "reviews", sessionSlug)
	} else if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	artifactPaths := nativeCoreReviewGuardPaths(directory, review.Report{}, statePath)
	resolvedPaths, err := review.CheckArtifactPaths(root, artifactPaths)
	if err != nil {
		return err
	}
	requestedBase := *base
	if requestedBase == "" {
		requestedBase, err = nativeCoreDefaultReviewBase(root)
		if err != nil {
			return err
		}
	}
	state, err := review.LoadIncrementalState(root, statePath, requestedBase, *full)
	if err != nil {
		return err
	}
	manifest, err := review.BuildIncrementalManifest(root, state, review.ManifestOptions{Worktree: *includeWorktree, CohortFiles: *cohortFiles})
	if err != nil {
		return err
	}
	tablePayload, err := os.ReadFile(filepath.Join(root, ".batuta", "routing.md"))
	if err != nil {
		return errors.New("review: .batuta/routing.md is missing — run /batuta-init first")
	}
	table, err := routing.ParseRoutingTable(tablePayload)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}
	runtime, err := review.ReviewerRuntimeFromTable(table, routing.DomainGeneral, *reviewer)
	if err != nil {
		return err
	}
	cohorts, err := review.RunCohorts(ctx, manifest, runtime, options)
	if err != nil {
		return nativeCoreReviewSessionError(ctx, git, root, baseline, trackedBaseline, err)
	}
	var sweep *review.SpecSweep
	if *spec != "" {
		result, err := review.RunSpecSweep(ctx, manifest, sweepRules, runtime, options)
		if err != nil {
			return nativeCoreReviewSessionError(ctx, git, root, baseline, trackedBaseline, err)
		}
		result.Results = review.MergeSpecResults(rules, proofResults, result.Results)
		sweep = &result
	}
	after, stateErr := git.WorktreeState(ctx, root)
	if stateErr != nil {
		return fmt.Errorf("review: verify source tree: %w", stateErr)
	}
	if after != baseline {
		paths := nativeCoreChangedTrackedPaths(root, baseline.HeadSHA, trackedBaseline)
		if len(paths) == 0 {
			paths = []string{"worktree state changed"}
		}
		return fmt.Errorf("review: source tree changed during review: %s", strings.Join(paths, ", "))
	}
	report := review.BuildReport(manifest, cohorts, sweep)
	state = review.StateAfterReport(report, after.HeadSHA)
	artifactPaths = nativeCoreReviewGuardPaths(directory, report, statePath)
	resolvedPaths, err = review.CheckArtifactPaths(root, artifactPaths)
	if err != nil {
		return err
	}
	publicationGit := git
	publicationGit.Runner = nativeCoreReviewPublicationRunner{paths: resolvedPaths, runner: git.Runner}
	beforePublication, err := publicationGit.WorktreeState(ctx, root)
	if err != nil {
		return err
	}
	if err := review.WriteArtifacts(directory, report, state); err != nil {
		return err
	}
	if err := nativeCoreReviewSessionError(ctx, publicationGit, root, beforePublication, trackedBaseline, nil); err != nil {
		return err
	}
	if err := review.WriteIncrementalState(statePath, state); err != nil {
		return err
	}
	if err := nativeCoreReviewSessionError(ctx, publicationGit, root, beforePublication, trackedBaseline, nil); err != nil {
		return err
	}
	if err := review.PrintReport(stdout, report); err != nil {
		return err
	}
	switch report.Verdict {
	case review.Ship:
		return nil
	case review.FixBeforeShip:
		return &nativeCoreReviewExit{code: 2}
	default:
		return &nativeCoreReviewExit{code: 3}
	}
}
