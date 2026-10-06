package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func nativeApprovalFixture(t *testing.T) (*Service, domain.PlanApproval, domain.MissionBinding, string) {
	t.Helper()
	svc, path := approvalFixture(t)
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	b := a.Identity.Binding
	b.DeliveryID = batuta.ExecutionDelivery(a)
	js, err := journal.Open(b.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	detailBytes, err := json.Marshal(map[string]string{"slug": b.PlanSlug, "plan_path": b.PlanPath, "plan_digest": b.PlanDigest, "workspace": b.CanonicalRoot, "approval_receipt_id": a.ID, "plan_content_digest": a.Identity.Version})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: detailBytes, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	b.OpenedDigest = opened.Digest
	if err := svc.Store.BindMission("m", b); err != nil {
		t.Fatal(err)
	}
	return svc, a, b, path
}

func TestNativeWatchUsesDurableReceiptAfterSourceRemoval(t *testing.T) {
	svc, _, b, path := nativeApprovalFixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	view, err := svc.WatchMission("m", "w", b.DeliveryID)
	if err != nil || !view.Available || view.Source != domain.SourceCurrent {
		t.Fatal("native watch lost approved source", view, err)
	}
	if _, err := svc.WatchMission("other", "w", b.DeliveryID); err == nil {
		t.Fatal("other mission observed native delivery")
	}
	if _, err := os.Stat(filepath.Join(b.CanonicalRoot, b.PlanPath)); !os.IsNotExist(err) {
		t.Fatal("watch reconstructed mutable plan", err)
	}
}
