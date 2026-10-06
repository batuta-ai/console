package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/routing"
)

func (s *Service) planBinding(ctx context.Context, mission, workspace string) (domain.MissionBinding, error) {
	if err := ctx.Err(); err != nil {
		return domain.MissionBinding{}, err
	}
	if mission == "" || workspace == "" {
		return domain.MissionBinding{}, domain.ErrInvalidReference
	}
	m, ws, err := s.missionWorkspace(mission, workspace)
	if err != nil {
		return domain.MissionBinding{}, err
	}
	if m.Binding == nil || m.Binding.WorkspaceID != ws.ID || m.Binding.WorkspaceRoot != ws.Root {
		return domain.MissionBinding{}, domain.ErrBindingConflict
	}
	return *m.Binding, nil
}

func readBoundPlan(b domain.MissionBinding) (routing.Plan, []byte, error) {
	canonical, err := batuta.CanonicalWorkspace(b.WorkspaceRoot)
	if err != nil {
		return routing.Plan{}, nil, err
	}
	if canonical != b.CanonicalRoot {
		return routing.Plan{}, nil, domain.ErrBindingConflict
	}
	plan, body, err := batuta.ExactPlanContent(canonical, b.PlanPath)
	if err != nil {
		return plan, nil, err
	}
	if plan.Slug != b.PlanSlug || plan.Set.Digest != b.PlanDigest {
		return plan, nil, domain.ErrBindingConflict
	}
	return plan, body, nil
}

func (s *Service) PlanDetail(ctx context.Context, mission, workspace string) (domain.PlanDetail, error) {
	var out domain.PlanDetail
	b, err := s.planBinding(ctx, mission, workspace)
	if err != nil {
		return out, err
	}
	if b.DeliveryID != "" {
		detail, native, err := s.nativePlanDetail(ctx, mission, workspace, b)
		if native || err != nil {
			return detail, err
		}
	}
	p, content, err := readBoundPlan(b)
	if err != nil {
		return out, err
	}
	out = describePlan(domain.PlanIdentity{MissionID: mission, Binding: b, Version: domain.PlanVersion(content)}, p, content)
	receipt, err := s.Store.FindPlanApproval(ctx, out.Identity)
	if err == nil {
		out.Approval = &receipt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.PlanDetail{}, err
	}
	return out, nil
}

func describePlan(identity domain.PlanIdentity, p routing.Plan, content []byte) domain.PlanDetail {
	out := domain.PlanDetail{Identity: identity, Content: content, Title: p.Title, Goal: p.Goal, Context: p.Context, Created: p.Created, Inputs: p.Inputs, Status: string(p.Status), Tasks: make([]domain.PlanTask, 0, len(p.Tasks))}
	for _, t := range p.Tasks {
		out.Tasks = append(out.Tasks, domain.PlanTask{ID: t.ID, Number: t.Number, Title: t.Title, Status: t.Status, Domain: string(t.Domain), Complexity: string(t.Complexity), Dependencies: t.Dependencies, Executor: t.Executor, Model: t.Model, Scope: t.Scope, Accept: t.Accept, Line: t.Line})
	}
	return out
}

func (s *Service) ApprovePlan(ctx context.Context, identity domain.PlanIdentity) (domain.PlanApproval, error) {
	if err := identity.Validate(); err != nil {
		return domain.PlanApproval{}, err
	}
	b, err := s.planBinding(ctx, identity.MissionID, identity.Binding.WorkspaceID)
	if err != nil {
		return domain.PlanApproval{}, err
	}
	if b != identity.Binding {
		return domain.PlanApproval{}, domain.ErrBindingConflict
	}
	if b.DeliveryID != "" || strings.Contains(b.PlanPath, "/done/") {
		return domain.PlanApproval{}, domain.ErrPlanTransition
	}
	return s.Store.ApprovePlan(ctx, identity, func() ([]byte, error) {
		p, body, err := readBoundPlan(b)
		if err != nil {
			return nil, err
		}
		if domain.PlanVersion(body) != identity.Version {
			return nil, domain.ErrPlanStale
		}
		if p.Status != routing.PlanProposed && p.Status != routing.PlanApproved {
			return nil, domain.ErrPlanTransition
		}
		return body, nil
	})
}

// Historical retrieval is mission/workspace scoped and independent of today's
// editable source or binding. Task21 must validate execution eligibility anew.
func (s *Service) PlanApproval(ctx context.Context, mission, workspace, id string) (domain.PlanApproval, error) {
	if err := ctx.Err(); err != nil {
		return domain.PlanApproval{}, err
	}
	if mission == "" || workspace == "" || id == "" {
		return domain.PlanApproval{}, domain.ErrInvalidReference
	}
	if _, _, err := s.missionWorkspace(mission, workspace); err != nil {
		return domain.PlanApproval{}, err
	}
	return s.Store.GetPlanApproval(ctx, mission, workspace, id)
}
