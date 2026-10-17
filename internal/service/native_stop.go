package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
)

// StopExecution does not lend ownership to the HTTP caller. Exact invocation
// selection is atomic; catalog validation never holds the admission lock.
func (s *Service) StopExecution(ctx context.Context, req domain.ExecutionStopRequest) (domain.ExecutionStopResult, error) {
	out := domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopRejected}
	if err := req.Validate(); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if s.native == nil {
		return out, domain.ErrExecutionUnavailable
	}
	receipt, err := s.PlanApproval(ctx, req.Target.MissionID, req.Target.Binding.WorkspaceID, req.ApprovalID)
	if err != nil {
		return out, err
	}
	if receipt.Identity != req.Target || domain.PlanVersion(receipt.Content) != req.Target.Version {
		return out, domain.ErrInvalidReference
	}
	b, err := s.planBinding(ctx, req.Target.MissionID, req.Target.Binding.WorkspaceID)
	if err != nil {
		return out, err
	}
	if b.DeliveryID != "" && b.DeliveryID != req.Delivery {
		return out, domain.ErrBindingConflict
	}
	b.DeliveryID, b.OpenedDigest = "", ""
	canonical, err := batuta.CanonicalWorkspace(b.WorkspaceRoot)
	if err != nil {
		return out, err
	}
	if b != req.Target.Binding || canonical != b.CanonicalRoot {
		return out, domain.ErrBindingConflict
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	n := s.native
	n.mu.Lock()
	defer n.mu.Unlock()
	job := n.jobs[req.Delivery]
	if job == nil || job.runID != req.RunID || job.request.Target != req.Target || job.request.ApprovalID != req.ApprovalID {
		for _, prior := range n.completed {
			if prior.ExecutionStopRequest == req {
				return prior, nil
			}
		}
		return out, domain.ErrInvalidReference
	}
	job.cancel()
	out.Effect = domain.ExecutionStopPending
	return out, nil
}

func (job *nativeExecution) recordRun(state string, err error) {
	job.outcomeMu.Lock()
	defer job.outcomeMu.Unlock()
	if state != "" {
		job.coreState = state
	}
	job.runErr = errors.Join(job.runErr, err)
}

func (s *Service) recordExecutionCleanup(job *nativeExecution, err error) {
	if err == nil {
		return
	}
	job.outcomeMu.Lock()
	job.cleanupErr = errors.Join(job.cleanupErr, err)
	job.outcomeMu.Unlock()
	s.recordNativeCleanup(err)
}

// A joined cancellation must not conceal a second, independent failure.
func onlyNativeCancellation(err error) bool {
	if err == nil {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if !onlyNativeCancellation(cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		inner := wrapped.Unwrap()
		return inner != nil && onlyNativeCancellation(inner)
	}
	return err == context.Canceled
}

func (s *Service) finishNativeExecution(job *nativeExecution, receipt domain.PlanApproval) {
	// This defer runs after all prepare/run resource defers. The read budget is
	// independent of both the canceled native context and the Stop caller.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(job.ctx), 5*time.Second)
	defer cancel()
	out := domain.ExecutionStopResult{ExecutionStopRequest: domain.ExecutionStopRequest{Target: receipt.Identity, ApprovalID: receipt.ID, Delivery: job.delivery, RunID: job.runID}, Effect: domain.ExecutionStopUncertain, Finalized: true}
	job.outcomeMu.Lock()
	out.CoreState = job.coreState
	runErr, cleanupErr := job.runErr, job.cleanupErr
	job.outcomeMu.Unlock()
	state, verifyErr := s.ExecutionState(ctx, receipt.Identity.MissionID, receipt.Identity.Binding.WorkspaceID, receipt.ID)
	if verifyErr == nil {
		state.Active, state.RunID = false, ""
		out.State = &state
		if state.OpenedDigest == "" {
			exists, err := batuta.DeliveryEvidenceExists(receipt.Identity.Binding.CanonicalRoot, job.delivery)
			verifyErr = err
			if exists || out.CoreState != "" {
				verifyErr = errors.Join(verifyErr, fmt.Errorf("native delivery evidence is incomplete"))
			}
		} else if !state.Bound || state.Watch == nil || out.CoreState == "" {
			verifyErr = fmt.Errorf("native final state is not verified")
		} else {
			binding := receipt.Identity.Binding
			binding.DeliveryID, binding.OpenedDigest = job.delivery, state.OpenedDigest
			evidence, err := batuta.ObserveApprovedExecutionContext(ctx, binding.CanonicalRoot, binding, receipt)
			verifyErr = err
			if err == nil {
				view := evidence.View
				view.Scope, view.MissionID, view.WorkspaceID, view.Workspace = "mission", job.mission, binding.WorkspaceID, binding.WorkspaceRoot
				if view.PendingQuestion != nil {
					view.PendingQuestion.Target.MissionID, view.PendingQuestion.Target.WorkspaceID = job.mission, binding.WorkspaceID
				}
				state.Watch, state.Gate = &view, view.Progression
				out.State = &state
				if evidence.OpenedDigest != state.OpenedDigest || evidence.TerminalDigest == "" || evidence.TerminalState != out.CoreState || evidence.CleanupPending {
					verifyErr = fmt.Errorf("native terminal does not verify the returned state and cleanup")
				}
			}
		}
	}
	out.Durable = verifyErr == nil
	if verifyErr == nil && out.State != nil && out.State.Gate != nil && out.CoreState == "done" && out.State.Gate.Required && !out.State.Gate.Cleared {
		verifyErr = fmt.Errorf("native review remains unresolved")
	}
	for _, err := range []error{runErr, cleanupErr, verifyErr} {
		if err != nil {
			out.Causes = append(out.Causes, err.Error())
		}
	}
	if verifyErr == nil && cleanupErr == nil && onlyNativeCancellation(runErr) {
		out.Effect = domain.ExecutionStopCompleted
	} else {
		out.Error = &domain.PlanFailure{Code: "execution_stop_uncertain", Message: "native finalization requires inspection"}
	}
	n := s.native
	n.mu.Lock()
	// Bounded observational history confers no cancellation authority.
	if len(n.completed) == 64 {
		copy(n.completed, n.completed[1:])
		n.completed = n.completed[:63]
	}
	n.completed = append(n.completed, out)
	n.mu.Unlock()
	n.finish(job)
}
