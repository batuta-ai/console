package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

// ApprovePlan serializes ownership validation, bounded source validation and
// receipt/event persistence. read must not access this Store (one connection).
// External writers remain free to change the source after read; only the bytes
// returned here can enter the immutable snapshot.
func (s *Store) ApprovePlan(ctx context.Context, identity domain.PlanIdentity, read func() ([]byte, error)) (domain.PlanApproval, error) {
	var empty domain.PlanApproval
	if err := identity.Validate(); err != nil {
		return empty, err
	}
	if identity.Binding.DeliveryID != "" {
		return empty, domain.ErrPlanTransition
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE missions SET id=id WHERE id=?`, identity.MissionID); err != nil {
		return empty, err
	}
	var b domain.MissionBinding
	var root string
	err = tx.QueryRowContext(ctx, `SELECT m.workspace_id,w.root,m.binding_root,m.binding_canonical_root,m.plan_path,m.plan_slug,m.plan_digest,m.delivery_id,m.opened_digest FROM missions m JOIN workspaces w ON w.id=m.workspace_id WHERE m.id=?`, identity.MissionID).Scan(&b.WorkspaceID, &root, &b.WorkspaceRoot, &b.CanonicalRoot, &b.PlanPath, &b.PlanSlug, &b.PlanDigest, &b.DeliveryID, &b.OpenedDigest)
	if err != nil {
		return empty, err
	}
	if b != identity.Binding || root != b.WorkspaceRoot {
		return empty, domain.ErrBindingConflict
	}
	content, err := read()
	if err != nil {
		return empty, err
	}
	if len(content) == 0 || len(content) > domain.MaxPlanBytes {
		return empty, domain.ErrInvalidReference
	}
	if domain.PlanVersion(content) != identity.Version {
		return empty, domain.ErrPlanStale
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return empty, err
	}
	prior, err := scanPlanApproval(tx.QueryRowContext(ctx, `SELECT id,identity,content,approved_at FROM plan_approvals WHERE identity=?`, string(encoded)))
	if err == nil {
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	receipt := domain.PlanApproval{ID: domain.NewID("approval"), Identity: identity, Content: append([]byte(nil), content...), ApprovedAt: domain.Now()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_approvals(id,mission_id,workspace_id,identity,content,approved_at) VALUES(?,?,?,?,?,?)`, receipt.ID, identity.MissionID, b.WorkspaceID, string(encoded), receipt.Content, receipt.ApprovedAt.Format(time.RFC3339Nano)); err != nil {
		return empty, err
	}
	payload, err := json.Marshal(map[string]any{"receipt_id": receipt.ID, "identity": identity})
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(at,kind,payload) VALUES(?,?,?)`, receipt.ApprovedAt.Format(time.RFC3339Nano), "plan.approved", string(payload)); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, errors.Join(domain.ErrPlanApprovalUncertain, err)
	}
	return receipt, nil
}

func scanPlanApproval(row interface{ Scan(...any) error }) (domain.PlanApproval, error) {
	var a domain.PlanApproval
	var identity, at string
	if err := row.Scan(&a.ID, &identity, &a.Content, &at); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(identity), &a.Identity); err != nil {
		return domain.PlanApproval{}, err
	}
	var err error
	a.ApprovedAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return domain.PlanApproval{}, err
	}
	if err := a.Identity.Validate(); err != nil {
		return domain.PlanApproval{}, err
	}
	if domain.PlanVersion(a.Content) != a.Identity.Version {
		return domain.PlanApproval{}, domain.ErrPlanStale
	}
	return a, nil
}

func (s *Store) GetPlanApproval(ctx context.Context, mission, workspace, id string) (domain.PlanApproval, error) {
	return scanPlanApproval(s.db.QueryRowContext(ctx, `SELECT id,identity,content,approved_at FROM plan_approvals WHERE id=? AND mission_id=? AND workspace_id=?`, id, mission, workspace))
}

func (s *Store) FindPlanApproval(ctx context.Context, identity domain.PlanIdentity) (domain.PlanApproval, error) {
	encoded, err := json.Marshal(identity)
	if err != nil {
		return domain.PlanApproval{}, err
	}
	return scanPlanApproval(s.db.QueryRowContext(ctx, `SELECT id,identity,content,approved_at FROM plan_approvals WHERE identity=?`, string(encoded)))
}
