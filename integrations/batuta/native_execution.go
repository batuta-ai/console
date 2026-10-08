package batuta

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

// A receipt names one native delivery across daemon restarts and lost replies.
// This identity never selects a different journal or implies that Run started.
func ExecutionDelivery(a domain.PlanApproval) string {
	return domain.ExecutionDelivery(a.ID, a.Identity)
}

type DeliveryApproval struct {
	ReceiptID string `json:"approval_receipt_id"`
	Version   string `json:"plan_content_digest"`
}

// Existence is only uncertainty evidence. It does not validate an opening,
// follow a symlink target, create a journal or choose another delivery.
func DeliveryEvidenceExists(workspace, delivery string) (bool, error) {
	if !journal.ValidDeliveryID(delivery) {
		return false, domain.ErrInvalidReference
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return false, artifactError(err)
	}
	defer root.Close()
	_, err = root.Lstat(journal.Dir + "/" + delivery + ".jsonl")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, artifactError(err)
	}
	return true, nil
}

func DeliveryApprovalReference(workspace string, b domain.MissionBinding) (DeliveryApproval, error) {
	records, err := deliveryRecords(workspace, b)
	if err != nil {
		return DeliveryApproval{}, err
	}
	var out DeliveryApproval
	if json.Unmarshal(records[0].Detail, &out) != nil || (out.ReceiptID == "") != (out.Version == "") {
		return out, ErrArtifactInvalid
	}
	return out, nil
}

func approvedPlan(b domain.MissionBinding, a domain.PlanApproval) (routing.Plan, error) {
	base := b
	base.DeliveryID, base.OpenedDigest = "", ""
	if a.ID == "" || a.ApprovedAt.IsZero() || a.Identity.Validate() != nil || a.Identity.Binding != base || len(a.Content) == 0 || len(a.Content) > domain.MaxPlanBytes || domain.PlanVersion(a.Content) != a.Identity.Version || b.DeliveryID != ExecutionDelivery(a) {
		return routing.Plan{}, ErrArtifactInvalid
	}
	p, err := routing.ParsePlan(b.PlanSlug, a.Content)
	if err != nil || p.Set.Digest != b.PlanDigest {
		return routing.Plan{}, ErrArtifactInvalid
	}
	p.Path = b.PlanPath
	return p, nil
}

func ValidateApprovedDelivery(workspace string, b domain.MissionBinding, a domain.PlanApproval) (string, error) {
	if _, err := approvedPlan(b, a); err != nil {
		return "", err
	}
	records, err := deliveryRecords(workspace, b)
	if err != nil {
		return "", err
	}
	var approval DeliveryApproval
	if json.Unmarshal(records[0].Detail, &approval) != nil || approval.ReceiptID != a.ID || approval.Version != a.Identity.Version {
		return "", ErrArtifactInvalid
	}
	return records[0].Digest, nil
}

// Native projection uses the original receipt and chain, independently of the
// editable plan path. PanelModel remains the only task/gate status authority.
func ProjectApprovedMission(workspace string, b domain.MissionBinding, a domain.PlanApproval) (domain.WatchView, error) {
	out := domain.WatchView{API: API, Workspace: workspace, Delivery: b.DeliveryID, Source: domain.SourceUnavailable}
	p, err := approvedPlan(b, a)
	if err != nil {
		return out, err
	}
	if _, err := ValidateApprovedDelivery(workspace, b, a); err != nil {
		return out, err
	}
	records, err := deliveryRecords(workspace, b)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	view := loop.PanelModel(records, now, "")
	view.Header.Presence, view.Header.Loops = loop.Presence(workspace, b.DeliveryID, now)
	out = dto(workspace, b.DeliveryID, view)
	gate, err := loop.CheckSupervisionGate(context.Background(), loop.SupervisionOptions{Workspace: workspace, Delivery: b.DeliveryID})
	if err != nil {
		return domain.WatchView{Source: domain.SourceUnavailable}, artifactError(err)
	}
	out.Progression = &domain.ExecutionGate{Required: gate.Required, Cleared: gate.Cleared, Delivery: gate.Delivery, ReviewID: gate.ReviewID, EvidenceDigest: gate.EvidenceDigest, Reason: gate.Reason}
	if gate.Required && !gate.Cleared && out.Header["state"] == loop.StateDone {
		out.Header["state"] = loop.StateReviewBlocked
	}
	out.PendingQuestion = pendingQuestion(records, out)
	out.Plans = []domain.PlanRef{{Name: p.Slug, Status: string(p.Status), Done: IsArchivedPlanPath(p.Path, p.Slug) || p.Status == routing.PlanDone}}
	out.Source = domain.SourceCurrent
	return out, nil
}
