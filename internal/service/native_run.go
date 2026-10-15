package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/inventory"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
)

type nativeLinkedRunner struct {
	runner publication.CommandRunner
	guard  func(context.Context) error
	active *atomic.Bool
}

func (r nativeLinkedRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	if r.active.Load() {
		if err := r.guard(ctx); err != nil {
			return publication.CommandResult{ExitCode: -1}, err
		}
	}
	return r.runner.Run(ctx, command)
}

type nativeOpeningWriter struct {
	ctx    context.Context
	guard  func(context.Context) error
	active *atomic.Bool
	once   sync.Once
	err    error
}

func (w *nativeOpeningWriter) Write(data []byte) (int, error) {
	if w.active.Load() {
		w.once.Do(func() { w.err = w.guard(w.ctx) })
		if w.err != nil {
			return 0, w.err
		}
	}
	return len(data), nil
}

func (s *Service) qualifiedNativeCore(ctx context.Context, runner publication.CommandRunner) (string, error) {
	path := s.CoreExecutable
	if path == "" {
		path = "batuta"
	}
	executable, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("%w: Core CLI is missing", domain.ErrExecutionUnavailable)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runner.Run(probeCtx, publication.Command{Executable: executable, Args: []string{"capabilities"}, StdoutLimit: 32 << 10})
	var capabilities struct {
		Version  string   `json:"version"`
		Commands []string `json:"commands"`
	}
	if err != nil || result.ExitCode != 0 || result.StdoutTruncated || json.Unmarshal(result.Stdout, &capabilities) != nil || strings.TrimPrefix(capabilities.Version, "v") != "1.1.0-beta.55" {
		return "", fmt.Errorf("%w: Core CLI must match qualified SDK v1.1.0-beta.55", domain.ErrExecutionUnavailable)
	}
	for _, required := range []string{"loop", "review", "inventory"} {
		found := false
		for _, command := range capabilities.Commands {
			if command == required {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("%w: Core CLI lacks %s", domain.ErrExecutionUnavailable, required)
		}
	}
	return executable, nil
}

func authorizeNativePreview(runner *loop.Runner, selected nativeAccounts) error {
	preview, err := runner.DryRun()
	if err != nil {
		return err
	}
	for _, wave := range preview.Waves {
		for _, task := range wave.Tasks {
			account, ok := selected.byCLI[task.Executor]
			if !ok || !account.permits(task.Model, task.Reasoning) {
				return fmt.Errorf("%w: native route %s/%s is not bound to the mission", domain.ErrInvalidReference, task.Executor, task.Model)
			}
			for _, fallback := range task.Fallbacks {
				cli, model, ok := strings.Cut(fallback, "/")
				if cli == "self" {
					continue
				}
				account, bound := selected.byCLI[cli]
				// Preview omits fallback effort. The final command boundary
				// still checks the selected account's exact model/effort argv.
				if !ok || !bound || !account.permitsModel(model) {
					return fmt.Errorf("%w: native fallback %s is not bound to the mission", domain.ErrInvalidReference, fallback)
				}
			}
		}
	}
	return nil
}

func (s *Service) runNativeExecution(job *nativeExecution, req domain.ExecutionRequest, receipt domain.PlanApproval, resume string) {
	defer s.native.finish(job)
	result := executionFailure(req, nil)
	fail := func(err error) { s.signalNativeExecutionFailure(job, receipt, result, false, err) }
	mission, err := s.Store.GetMission(job.mission)
	if err != nil {
		fail(err)
		return
	}
	selected, skills, err := s.nativeAccounts(job.ctx, mission, receipt.Identity.Binding.CanonicalRoot)
	if err != nil {
		fail(err)
		return
	}
	s.runPreparedNativeExecution(job, req, receipt, resume, selected, skills)
}

func (s *Service) signalNativeExecutionFailure(job *nativeExecution, receipt domain.PlanApproval, result domain.ExecutionResult, active bool, err error) {
	b := receipt.Identity.Binding
	b.DeliveryID = job.delivery
	if digest, readErr := batuta.ValidateApprovedDelivery(b.CanonicalRoot, b, receipt); readErr == nil {
		result.Delivery = job.delivery
		result.OpenedDigest = digest
		err = errors.Join(domain.ErrExecutionUncertain, err)
		result.Effect = domain.ExecutionUncertain
	} else if exists, existenceErr := batuta.DeliveryEvidenceExists(b.CanonicalRoot, job.delivery); exists || existenceErr != nil || active {
		result.Delivery = job.delivery
		result.Effect = domain.ExecutionUncertain
		err = errors.Join(domain.ErrExecutionUncertain, err, existenceErr)
	}
	job.signal(result, err)
}

func (s *Service) linkNativeExecution(ctx context.Context, job *nativeExecution, receipt domain.PlanApproval, result *domain.ExecutionResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base, err := s.planBinding(ctx, job.mission, receipt.Identity.Binding.WorkspaceID)
	if err != nil {
		return err
	}
	canonical, err := batuta.CanonicalWorkspace(base.WorkspaceRoot)
	if err != nil {
		return err
	}
	if canonical != receipt.Identity.Binding.CanonicalRoot {
		return domain.ErrBindingConflict
	}
	b := receipt.Identity.Binding
	b.DeliveryID = job.delivery
	b.OpenedDigest, err = batuta.ValidateApprovedDelivery(b.CanonicalRoot, b, receipt)
	if err != nil {
		return err
	}
	if err := s.Store.AttachApprovedExecution(ctx, receipt.ID, receipt.Identity, b); err != nil {
		return err
	}
	result.Delivery = job.delivery
	result.OpenedDigest = b.OpenedDigest
	result.Effect = domain.ExecutionStarted
	job.signal(*result, nil)
	return nil
}

func (s *Service) runPreparedNativeExecution(job *nativeExecution, req domain.ExecutionRequest, receipt domain.PlanApproval, resume string, selected nativeAccounts, skills string) {
	result := executionFailure(req, nil)
	var runActive atomic.Bool
	fail := func(err error) { s.signalNativeExecutionFailure(job, receipt, result, runActive.Load(), err) }
	capacities := map[string]int{}
	for _, account := range selected.byCLI {
		capacities[account.account.ID] = account.account.SessionCap
	}
	release, err := s.Session.ReserveNative(capacities)
	if err != nil {
		fail(err)
		return
	}
	defer release()
	privateEnv, home, err := accounts.SessionEnvironment()
	if err != nil {
		fail(err)
		return
	}
	defer func() {
		if err := os.RemoveAll(home); err != nil {
			s.recordNativeCleanup(err)
		}
	}()
	var linkMu sync.Mutex
	guard := func(ctx context.Context) error {
		linkMu.Lock()
		defer linkMu.Unlock()
		return s.linkNativeExecution(ctx, job, receipt, &result)
	}
	if handled, state, runErr := s.runNativeWindowsExecution(job, req, receipt, resume, selected, skills, privateEnv, &runActive, guard); handled {
		if runErr == nil {
			runErr = guard(job.ctx)
		}
		if runErr != nil {
			fail(runErr)
		}
		if _, err := s.Store.AppendEvent("execution.finished", map[string]any{"mission_id": job.mission, "workspace_id": receipt.Identity.Binding.WorkspaceID, "approval_id": receipt.ID, "delivery": job.delivery, "state": state, "failed": runErr != nil}); err != nil {
			s.recordNativeCleanup(err)
		}
		return
	}
	commandRunner := session.NativeRunner{Manager: s.Session, Environment: privateEnv}
	core, err := s.qualifiedNativeCore(job.ctx, commandRunner)
	if err != nil {
		fail(err)
		return
	}
	reviewEnv, err := s.nativeReviewEnvironment(home, privateEnv, selected)
	if err != nil {
		fail(err)
		return
	}
	transport := &executor.TransportBackend{Mode: "cli"}
	accountRunner := nativeAccountRunner{accounts: selected, other: commandRunner}
	opts := loop.Options{
		Workspace: receipt.Identity.Binding.CanonicalRoot, Skills: skills, Plan: receipt.Identity.Binding.PlanPath,
		ApprovedSnapshot: &loop.ApprovedPlanSnapshot{Slug: receipt.Identity.Binding.PlanSlug, Path: receipt.Identity.Binding.PlanPath, ReceiptID: receipt.ID, ContentDigest: receipt.Identity.Version, Content: receipt.Content},
		DeliveryID:       job.delivery, Parallel: 1, KeepWorktrees: true, Supervision: true,
		Runner:      nativeLinkedRunner{runner: accountRunner, guard: guard, active: &runActive},
		Environment: []string{"GIT_CONFIG_COUNT=0"},
		Transport:   transport, VerifierTransport: transport,
		Inventory: func(ctx context.Context) (snapshot inventory.InventorySnapshot, err error) {
			return selected.inventory(ctx, receipt.Identity.Binding.CanonicalRoot)
		},
		Stdout:     &nativeOpeningWriter{ctx: job.ctx, guard: guard, active: &runActive},
		Supervisor: &loop.SuperviseOptions{Interval: 200 * time.Millisecond, Review: &loop.SupervisionReviewOptions{Executable: core, Skills: skills, Runner: nativeReviewRunner{runner: session.NativeRunner{Manager: s.Session, Environment: reviewEnv, Graceful: true}, accounts: selected, guard: guard, cleanup: s.recordNativeCleanup}}},
	}
	var runner *loop.Runner
	if resume != "" {
		opts.DeliveryID = ""
		opts.Resume = resume
		runner, err = loop.Resume(job.ctx, opts)
	} else {
		runner, err = loop.New(job.ctx, opts)
	}
	if err != nil {
		fail(err)
		return
	}
	defer func() {
		if err := runner.Release(); err != nil {
			s.recordNativeCleanup(err)
		}
	}()
	if err := authorizeNativePreview(runner, selected); err != nil {
		fail(err)
		return
	}
	if resume == "" {
		if _, err := s.executionApproval(job.ctx, req); err != nil {
			fail(err)
			return
		}
	}
	runActive.Store(true)
	state, runErr := runner.Run(job.ctx)
	if runErr != nil {
		fail(runErr)
	} else {
		if err := guard(job.ctx); err != nil {
			fail(err)
		}
	}
	if _, err := s.Store.AppendEvent("execution.finished", map[string]any{"mission_id": job.mission, "workspace_id": receipt.Identity.Binding.WorkspaceID, "approval_id": receipt.ID, "delivery": job.delivery, "state": state, "failed": runErr != nil}); err != nil {
		s.recordNativeCleanup(err)
	}
}
