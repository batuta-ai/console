package store

import (
	"database/sql"

	"github.com/batuta-ai/console/internal/domain"
)

// BindMission changes only binding columns. Ownership, immutability and the
// event are decided on the same connection and committed in one transaction.
func (s *Store) BindMission(id string, binding domain.MissionBinding) error {
	if binding.WorkspaceID == "" || binding.WorkspaceRoot == "" || binding.CanonicalRoot == "" || binding.PlanPath == "" || binding.PlanSlug == "" || binding.PlanDigest == "" || (binding.DeliveryID == "") != (binding.OpenedDigest == "") {
		return domain.ErrInvalidReference
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire SQLite's write reservation before reading, including across Stores.
	if _, err = tx.Exec(`UPDATE missions SET id=id WHERE id=?`, id); err != nil {
		return err
	}
	var prior domain.MissionBinding
	var root string
	err = tx.QueryRow(`SELECT m.workspace_id,w.root,m.binding_root,m.binding_canonical_root,m.plan_path,m.plan_slug,m.plan_digest,m.delivery_id,m.opened_digest FROM missions m JOIN workspaces w ON w.id=m.workspace_id WHERE m.id=?`, id).Scan(&prior.WorkspaceID, &root, &prior.WorkspaceRoot, &prior.CanonicalRoot, &prior.PlanPath, &prior.PlanSlug, &prior.PlanDigest, &prior.DeliveryID, &prior.OpenedDigest)
	if err != nil {
		return err
	}
	if prior.WorkspaceID != binding.WorkspaceID || root != binding.WorkspaceRoot {
		return domain.ErrInvalidReference
	}
	if prior == binding {
		return nil
	}
	if prior.DeliveryID != "" {
		return domain.ErrBindingConflict
	}
	if binding.DeliveryID != "" {
		var owner string
		err = tx.QueryRow(`SELECT id FROM missions WHERE (workspace_id=? OR binding_canonical_root=?) AND delivery_id=? AND id<>?`, binding.WorkspaceID, binding.CanonicalRoot, binding.DeliveryID, id).Scan(&owner)
		if err == nil {
			return domain.ErrBindingConflict
		}
		if err != sql.ErrNoRows {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE missions SET binding_root=?,binding_canonical_root=?,plan_path=?,plan_slug=?,plan_digest=?,delivery_id=?,opened_digest=? WHERE id=?`, binding.WorkspaceRoot, binding.CanonicalRoot, binding.PlanPath, binding.PlanSlug, binding.PlanDigest, binding.DeliveryID, binding.OpenedDigest, id); err != nil {
		return err
	}
	payload := map[string]any{"mission_id": id, "workspace_id": binding.WorkspaceID, "workspace_root": binding.WorkspaceRoot, "canonical_root": binding.CanonicalRoot, "plan_path": binding.PlanPath, "plan_slug": binding.PlanSlug, "plan_digest": binding.PlanDigest, "delivery_id": binding.DeliveryID, "opened_digest": binding.OpenedDigest, "previous": prior}
	if _, err = (catalogWriter{db: tx}).AppendEvent("mission.bound", payload); err != nil {
		return err
	}
	return tx.Commit()
}
