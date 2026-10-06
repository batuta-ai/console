package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/batuta-ai/console/internal/domain"
)

// AttachApprovedExecution compares the original receipt's binding under the
// SQLite write reservation. A concurrent reassignment cannot be reverted by a
// native opening, and an identical attachment emits its event only once.
func (s *Store) AttachApprovedExecution(ctx context.Context, receiptID string, identity domain.PlanIdentity, target domain.MissionBinding) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	base := target
	base.DeliveryID, base.OpenedDigest = "", ""
	if receiptID == "" || identity.Binding.DeliveryID != "" || base != identity.Binding || target.DeliveryID == "" || target.OpenedDigest == "" {
		return domain.ErrInvalidReference
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE missions SET id=id WHERE id=?`, identity.MissionID); err != nil {
		return err
	}
	receipt, err := scanPlanApproval(tx.QueryRowContext(ctx, `SELECT id,identity,content,approved_at FROM plan_approvals WHERE id=? AND mission_id=? AND workspace_id=?`, receiptID, identity.MissionID, identity.Binding.WorkspaceID))
	if err != nil {
		return err
	}
	if receipt.Identity != identity {
		return domain.ErrExecutionApprovalRequired
	}
	var prior domain.MissionBinding
	var root string
	err = tx.QueryRowContext(ctx, `SELECT m.workspace_id,w.root,m.binding_root,m.binding_canonical_root,m.plan_path,m.plan_slug,m.plan_digest,m.delivery_id,m.opened_digest FROM missions m JOIN workspaces w ON w.id=m.workspace_id WHERE m.id=?`, identity.MissionID).Scan(&prior.WorkspaceID, &root, &prior.WorkspaceRoot, &prior.CanonicalRoot, &prior.PlanPath, &prior.PlanSlug, &prior.PlanDigest, &prior.DeliveryID, &prior.OpenedDigest)
	if err != nil {
		return err
	}
	if root != identity.Binding.WorkspaceRoot {
		return domain.ErrBindingConflict
	}
	if prior == target {
		return nil
	}
	if prior != identity.Binding {
		return domain.ErrBindingConflict
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT id FROM missions WHERE (workspace_id=? OR binding_canonical_root=?) AND delivery_id=? AND id<>?`, target.WorkspaceID, target.CanonicalRoot, target.DeliveryID, identity.MissionID).Scan(&owner)
	if err == nil {
		return domain.ErrBindingConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE missions SET delivery_id=?,opened_digest=? WHERE id=?`, target.DeliveryID, target.OpenedDigest, identity.MissionID); err != nil {
		return err
	}
	if _, err := (catalogWriter{db: tx}).AppendEvent("execution.bound", map[string]any{"approval_id": receiptID, "target": identity, "delivery": target.DeliveryID, "opened_digest": target.OpenedDigest}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(domain.ErrExecutionUncertain, err)
	}
	return nil
}
