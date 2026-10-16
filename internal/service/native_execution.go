package service

import (
	"context"
	"errors"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/routing"
)

func (s *Service) projectMission(ctx context.Context, mission, workspace string, b domain.MissionBinding) (domain.WatchView, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, 5*time.Second, batuta.ErrArtifactIO)
	defer cancel()
	if b.DeliveryID == "" {
		return batuta.ProjectMissionContext(ctx, b.CanonicalRoot, b)
	}
	reference, err := batuta.DeliveryApprovalReferenceContext(ctx, b.CanonicalRoot, b)
	if err != nil {
		return domain.WatchView{API: "v0", Workspace: b.WorkspaceRoot, Delivery: b.DeliveryID, Source: domain.SourceUnavailable}, err
	}
	if reference.ReceiptID == "" {
		return batuta.ProjectMissionContext(ctx, b.CanonicalRoot, b)
	}
	receipt, err := s.Store.GetPlanApproval(ctx, mission, workspace, reference.ReceiptID)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil && (errors.Is(err, cause) || errors.Is(err, ctx.Err())) {
			return domain.WatchView{Source: domain.SourceUnavailable}, cause
		}
		return domain.WatchView{API: "v0", Workspace: b.WorkspaceRoot, Delivery: b.DeliveryID, Source: domain.SourceUnavailable}, errors.Join(batuta.ErrArtifactInvalid, err)
	}
	if receipt.Identity.Version != reference.Version {
		return domain.WatchView{}, batuta.ErrArtifactInvalid
	}
	return batuta.ProjectApprovedMissionContext(ctx, b.CanonicalRoot, b, receipt)
}

func (s *Service) executionApproval(ctx context.Context, req domain.ExecutionRequest) (domain.PlanApproval, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanApproval{}, err
	}
	if req.ApprovalID == "" {
		return domain.PlanApproval{}, domain.ErrExecutionApprovalRequired
	}
	if err := req.Target.Validate(); err != nil {
		return domain.PlanApproval{}, err
	}
	b, err := s.planBinding(ctx, req.Target.MissionID, req.Target.Binding.WorkspaceID)
	if err != nil {
		return domain.PlanApproval{}, err
	}
	if b.DeliveryID != "" {
		return domain.PlanApproval{}, domain.ErrExecutionExists
	}
	if b != req.Target.Binding {
		return domain.PlanApproval{}, domain.ErrBindingConflict
	}
	receipt, err := s.PlanApproval(ctx, req.Target.MissionID, b.WorkspaceID, req.ApprovalID)
	if err != nil {
		return domain.PlanApproval{}, err
	}
	if receipt.Identity != req.Target || domain.PlanVersion(receipt.Content) != req.Target.Version {
		return domain.PlanApproval{}, domain.ErrExecutionApprovalRequired
	}
	p, body, err := readBoundPlan(b)
	if err != nil {
		return domain.PlanApproval{}, err
	}
	if domain.PlanVersion(body) != receipt.Identity.Version {
		return domain.PlanApproval{}, domain.ErrPlanStale
	}
	if batuta.IsArchivedPlanPath(b.PlanPath, b.PlanSlug) || (p.Status != routing.PlanProposed && p.Status != routing.PlanApproved) {
		return domain.PlanApproval{}, domain.ErrPlanTransition
	}
	return receipt, nil
}

func executionFailure(req domain.ExecutionRequest, err error) domain.ExecutionResult {
	effect := domain.ExecutionRejected
	if errors.Is(err, domain.ErrExecutionUncertain) {
		effect = domain.ExecutionUncertain
	}
	return domain.ExecutionResult{Target: req.Target, ApprovalID: req.ApprovalID, Effect: effect}
}

func (s *Service) StartExecution(ctx context.Context, req domain.ExecutionRequest) (domain.ExecutionResult, error) {
	receipt, err := s.executionApproval(ctx, req)
	if err != nil {
		return executionFailure(req, err), err
	}
	if s.native == nil || s.Session == nil {
		return executionFailure(req, domain.ErrExecutionUnavailable), domain.ErrExecutionUnavailable
	}
	job, err := s.native.admitExecution(req, batuta.ExecutionDelivery(receipt))
	if err != nil {
		return executionFailure(req, err), err
	}
	go s.runNativeExecution(job, req, receipt, "")
	select {
	case outcome := <-job.ready:
		return outcome.result, outcome.err
	case <-ctx.Done():
		result := executionFailure(req, domain.ErrExecutionUncertain)
		result.Delivery, result.RunID = job.delivery, job.runID
		return result, errors.Join(domain.ErrExecutionUncertain, ctx.Err())
	}
}

func (s *Service) ExecutionState(ctx context.Context, mission, workspace, approvalID string) (domain.ExecutionState, error) {
	var out domain.ExecutionState
	if approvalID == "" {
		return out, domain.ErrExecutionApprovalRequired
	}
	receipt, err := s.PlanApproval(ctx, mission, workspace, approvalID)
	if err != nil {
		return out, err
	}
	b, err := s.planBinding(ctx, mission, workspace)
	if err != nil {
		return out, err
	}
	base := b
	base.DeliveryID, base.OpenedDigest = "", ""
	if base != receipt.Identity.Binding {
		return out, domain.ErrBindingConflict
	}
	canonical, err := batuta.CanonicalWorkspace(b.WorkspaceRoot)
	if err != nil {
		return out, err
	}
	if canonical != b.CanonicalRoot {
		return out, domain.ErrBindingConflict
	}
	delivery := batuta.ExecutionDelivery(receipt)
	if b.DeliveryID != "" && b.DeliveryID != delivery {
		return out, domain.ErrBindingConflict
	}
	out = domain.ExecutionState{Target: receipt.Identity, ApprovalID: receipt.ID, Delivery: delivery, Bound: b.DeliveryID != ""}
	if s.native != nil {
		s.native.mu.Lock()
		job, active := s.native.jobs[delivery]
		out.Active = active
		if active {
			out.RunID = job.runID
		}
		s.native.mu.Unlock()
	}
	b.DeliveryID = delivery
	opened, err := batuta.ValidateApprovedDeliveryContext(ctx, canonical, b, receipt)
	if errors.Is(err, batuta.ErrArtifactMissing) && !out.Bound {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	b.OpenedDigest = opened
	out.OpenedDigest = opened
	view, err := batuta.ProjectApprovedMissionContext(ctx, canonical, b, receipt)
	if err != nil {
		return out, err
	}
	view.Scope, view.MissionID, view.WorkspaceID, view.Workspace = "mission", mission, workspace, b.WorkspaceRoot
	if view.PendingQuestion != nil {
		view.PendingQuestion.Target.MissionID, view.PendingQuestion.Target.WorkspaceID = mission, workspace
	}
	out.Watch = &view
	out.Gate = view.Progression
	return out, nil
}

func (s *Service) ResumeExecution(ctx context.Context, req domain.ResumeRequest) (domain.ExecutionResult, error) {
	start := domain.ExecutionRequest{Target: req.Target, ApprovalID: req.ApprovalID}
	reject := func(err error) (domain.ExecutionResult, error) { return executionFailure(start, err), err }
	if err := ctx.Err(); err != nil {
		return reject(err)
	}
	if req.ApprovalID == "" {
		return reject(domain.ErrExecutionApprovalRequired)
	}
	if req.Target.Validate() != nil || req.Delivery == "" || req.OpenedDigest == "" {
		return reject(domain.ErrInvalidReference)
	}
	state, err := s.ExecutionState(ctx, req.Target.MissionID, req.Target.Binding.WorkspaceID, req.ApprovalID)
	if err != nil {
		return reject(err)
	}
	if state.Target != req.Target || state.Delivery != req.Delivery || state.OpenedDigest != req.OpenedDigest {
		return reject(domain.ErrInvalidReference)
	}
	if batuta.IsArchivedPlanPath(state.Target.Binding.PlanPath, state.Target.Binding.PlanSlug) {
		return reject(domain.ErrPlanTransition)
	}
	receipt, err := s.PlanApproval(ctx, req.Target.MissionID, req.Target.Binding.WorkspaceID, req.ApprovalID)
	if err != nil {
		return reject(err)
	}
	if s.native == nil || s.Session == nil {
		return reject(domain.ErrExecutionUnavailable)
	}
	job, err := s.native.admitExecution(start, req.Delivery)
	if err != nil {
		return reject(err)
	}
	go s.runNativeExecution(job, start, receipt, req.Delivery)
	select {
	case outcome := <-job.ready:
		return outcome.result, outcome.err
	case <-ctx.Done():
		result := executionFailure(start, domain.ErrExecutionUncertain)
		result.Delivery, result.OpenedDigest, result.RunID = req.Delivery, req.OpenedDigest, job.runID
		return result, errors.Join(domain.ErrExecutionUncertain, ctx.Err())
	}
}
