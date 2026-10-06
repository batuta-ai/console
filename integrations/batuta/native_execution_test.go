package batuta

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func approvedProjectionFixture(t *testing.T) (string, domain.MissionBinding, domain.PlanApproval) {
	t.Helper()
	root := t.TempDir()
	canonical, err := CanonicalWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("# Plan — alpha\n**Status:** proposed\n- [ ] 1. Keep approved bytes — backend/medium\n  Accept: verified\n")
	plan, err := routing.ParsePlan("alpha", content)
	if err != nil {
		t.Fatal(err)
	}
	b := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: root, CanonicalRoot: canonical, PlanPath: filepath.ToSlash(routing.PlanPath("alpha")), PlanSlug: "alpha", PlanDigest: plan.Set.Digest}
	a := domain.PlanApproval{ID: "receipt-one", Identity: domain.PlanIdentity{MissionID: "m", Binding: b, Version: domain.PlanVersion(content)}, Content: content, ApprovedAt: domain.Now()}
	b.DeliveryID = ExecutionDelivery(a)
	js, err := journal.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]string{"slug": b.PlanSlug, "plan_path": b.PlanPath, "plan_digest": b.PlanDigest, "workspace": canonical, "approval_receipt_id": a.ID, "plan_content_digest": a.Identity.Version})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	b.OpenedDigest = opened.Digest
	if err := os.MkdirAll(filepath.Join(canonical, ".batuta/plans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, b.PlanPath), content, 0600); err != nil {
		t.Fatal(err)
	}
	return canonical, b, a
}

func TestApprovedProjectionSurvivesSourceEditsDeletionAndArchive(t *testing.T) {
	for _, change := range []string{"edit", "delete", "archive"} {
		t.Run(change, func(t *testing.T) {
			root, b, a := approvedProjectionFixture(t)
			path := filepath.Join(root, b.PlanPath)
			switch change {
			case "edit":
				if err := os.WriteFile(path, []byte("replacement without approved tasks"), 0600); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "archive":
				dir := filepath.Join(root, ".batuta/plans/done")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, filepath.Join(dir, "alpha.md")); err != nil {
					t.Fatal(err)
				}
			}
			js, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(js.Path(b.DeliveryID))
			if err != nil {
				t.Fatal(err)
			}
			view, err := ProjectApprovedMission(root, b, a)
			if err != nil || !view.Available || view.Source != domain.SourceCurrent || view.Delivery != b.DeliveryID {
				t.Fatal(view, err)
			}
			after, err := os.ReadFile(js.Path(b.DeliveryID))
			if err != nil || string(before) != string(after) {
				t.Fatal("projection mutated native journal", err)
			}
		})
	}
}

func TestApprovedProjectionRejectsForeignReceiptAndOpening(t *testing.T) {
	for _, change := range []string{"receipt", "bytes", "workspace", "delivery", "opening"} {
		t.Run(change, func(t *testing.T) {
			root, b, a := approvedProjectionFixture(t)
			switch change {
			case "receipt":
				a.ID = "other"
			case "bytes":
				a.Content = append(a.Content, []byte("\nchanged")...)
			case "workspace":
				a.Identity.Binding.WorkspaceID = "foreign"
			case "delivery":
				b.DeliveryID = "foreign"
			case "opening":
				b.OpenedDigest = "foreign"
			}
			if _, err := ProjectApprovedMission(root, b, a); !errors.Is(err, ErrArtifactInvalid) {
				t.Fatal("accepted mismatched approval/opening", err)
			}
		})
	}
}
