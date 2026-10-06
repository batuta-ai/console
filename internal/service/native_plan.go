package service

import (
	"context"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/routing"
)

// A bound native delivery identifies its original receipt for a fresh client.
// Read only that validated snapshot, independently of the mutable source path.
func (s *Service) nativePlanDetail(ctx context.Context, mission, workspace string, binding domain.MissionBinding) (domain.PlanDetail, bool, error) {
	var out domain.PlanDetail
	canonical, err := batuta.CanonicalWorkspace(binding.WorkspaceRoot)
	if err != nil {
		return out, false, err
	}
	if canonical != binding.CanonicalRoot {
		return out, false, domain.ErrBindingConflict
	}
	reference, err := batuta.DeliveryApprovalReference(canonical, binding)
	if err != nil || reference.ReceiptID == "" {
		return out, false, err
	}
	receipt, err := s.Store.GetPlanApproval(ctx, mission, workspace, reference.ReceiptID)
	if err != nil {
		return out, true, err
	}
	if _, err := batuta.ValidateApprovedDelivery(canonical, binding, receipt); err != nil {
		return out, true, err
	}
	plan, err := routing.ParsePlan(binding.PlanSlug, receipt.Content)
	if err != nil {
		return out, true, err
	}
	out = describePlan(receipt.Identity, plan, receipt.Content)
	out.Approval = &receipt
	return out, true, nil
}
