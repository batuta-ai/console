package service

import (
	"context"
	"errors"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
)

type MissionBindingRequest struct {
	WorkspaceID string `json:"workspace_id"`
	PlanPath    string `json:"plan_path"`
	DeliveryID  string `json:"delivery_id,omitempty"`
}

func (s *Service) BindMission(id string, req MissionBindingRequest) (domain.Mission, error) {
	return s.BindMissionContext(context.Background(), id, req)
}

func (s *Service) BindMissionContext(ctx context.Context, id string, req MissionBindingRequest) (domain.Mission, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, 5*time.Second, batuta.ErrArtifactIO)
	defer cancel()
	m, ws, err := s.missionWorkspace(id, req.WorkspaceID)
	if err != nil {
		return m, err
	}
	if req.WorkspaceID == "" || req.PlanPath == "" {
		return m, domain.ErrInvalidReference
	}
	if m.Binding != nil && m.Binding.DeliveryID != "" {
		if m.Binding.WorkspaceRoot == ws.Root && m.Binding.PlanPath == req.PlanPath && m.Binding.DeliveryID == req.DeliveryID {
			return m, nil
		}
		return m, domain.ErrBindingConflict
	}
	canonical, err := batuta.CanonicalWorkspace(ws.Root)
	if err != nil {
		return m, err
	}
	plan, err := batuta.ExactPlan(canonical, req.PlanPath)
	if err != nil {
		return m, err
	}
	binding := domain.MissionBinding{WorkspaceID: ws.ID, WorkspaceRoot: ws.Root, CanonicalRoot: canonical, PlanPath: plan.Path, PlanSlug: plan.Slug, PlanDigest: plan.Set.Digest, DeliveryID: req.DeliveryID}
	if req.DeliveryID != "" {
		binding.OpenedDigest, err = batuta.ValidateDeliveryContext(ctx, canonical, binding)
		if err != nil {
			return m, err
		}
	}
	if err := s.Store.BindMission(id, binding); err != nil {
		return m, catalogReferenceError{err}
	}
	m, err = s.Store.GetMission(id)
	if err != nil {
		return m, catalogReferenceError{err}
	}
	return m, nil
}

func (s *Service) missionWorkspace(id, workspaceID string) (domain.Mission, domain.Workspace, error) {
	m, err := s.Store.GetMission(id)
	if err != nil {
		return m, domain.Workspace{}, catalogReferenceError{err}
	}
	if workspaceID != "" && workspaceID != m.WorkspaceID {
		return m, domain.Workspace{}, domain.ErrInvalidReference
	}
	ws, err := s.Store.GetWorkspace(m.WorkspaceID)
	if err != nil {
		return m, ws, catalogReferenceError{err}
	}
	return m, ws, nil
}

// WatchMission never invokes workspace discovery or chooses a delivery.
func (s *Service) WatchMission(id, workspaceID, deliveryID string) (domain.WatchView, error) {
	return s.WatchMissionContext(context.Background(), id, workspaceID, deliveryID)
}

func (s *Service) WatchMissionContext(ctx context.Context, id, workspaceID, deliveryID string) (domain.WatchView, error) {
	m, ws, err := s.missionWorkspace(id, workspaceID)
	if err != nil {
		return domain.WatchView{}, err
	}
	out := domain.WatchView{API: "v0", Scope: "mission", MissionID: m.ID, WorkspaceID: ws.ID, Workspace: ws.Root, Source: domain.SourceUnavailable}
	if deliveryID != "" && (m.Binding == nil || deliveryID != m.Binding.DeliveryID) {
		return out, domain.ErrInvalidReference
	}
	if m.Binding == nil {
		out.Message = "mission_unassigned"
		return out, nil
	}
	if m.Binding.WorkspaceID != ws.ID || m.Binding.WorkspaceRoot != ws.Root {
		out.Message = "workspace_changed"
		return out, nil
	}
	canonical, err := batuta.CanonicalWorkspace(ws.Root)
	if err != nil {
		if errors.Is(err, batuta.ErrArtifactMissing) {
			out.Message = "workspace_unavailable"
			return out, nil
		}
		return out, err
	}
	if canonical != m.Binding.CanonicalRoot {
		out.Message = "workspace_changed"
		return out, nil
	}
	view, err := s.projectMission(ctx, m.ID, ws.ID, *m.Binding)
	view.Workspace = ws.Root
	view.MissionID = m.ID
	view.WorkspaceID = ws.ID
	view.Scope = "mission"
	if view.PendingQuestion != nil {
		view.PendingQuestion.Target.MissionID = m.ID
		view.PendingQuestion.Target.WorkspaceID = ws.ID
	}
	if cause := context.Cause(ctx); cause != nil && errors.Is(err, cause) {
		return view, cause
	}
	if errors.Is(err, batuta.ErrArtifactMissing) {
		view.Message = "artifact_missing"
		return view, nil
	}
	if errors.Is(err, batuta.ErrArtifactInvalid) {
		view.Message = "artifact_changed_or_invalid"
		return view, nil
	}
	return view, err
}
