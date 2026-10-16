package batuta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func approvedProjectionFixture(t *testing.T) (string, domain.MissionBinding, domain.PlanApproval) {
	t.Helper()
	return approvedProjectionFixtureForPlan(t, filepath.ToSlash(routing.PlanPath("alpha")), routing.PlanProposed)
}

func approvedProjectionFixtureForPlan(t *testing.T, path string, status routing.PlanStatus) (string, domain.MissionBinding, domain.PlanApproval) {
	t.Helper()
	root := t.TempDir()
	canonical, err := CanonicalWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(fmt.Sprintf("# Plan — alpha\n**Status:** %s\n- [ ] 1. Keep approved bytes — backend/medium\n  Accept: verified\n", status))
	plan, err := routing.ParsePlan("alpha", content)
	if err != nil {
		t.Fatal(err)
	}
	b := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: root, CanonicalRoot: canonical, PlanPath: path, PlanSlug: "alpha", PlanDigest: plan.Set.Digest}
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
	if err := os.MkdirAll(filepath.Dir(filepath.Join(canonical, b.PlanPath)), 0700); err != nil {
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

func TestApprovedProjectionPlanDone(t *testing.T) {
	active := filepath.ToSlash(routing.PlanPath("alpha"))
	archived := filepath.Join(".batuta", "plans", "done", "alpha.md")
	type planCase struct {
		name   string
		path   string
		status routing.PlanStatus
		done   bool
	}
	cases := []planCase{
		{"active", active, routing.PlanProposed, false},
		{"status-done", active, routing.PlanDone, true},
		{"archived-native-binding", archived, routing.PlanProposed, true},
	}
	if slash := filepath.ToSlash(archived); slash != archived {
		cases = append(cases, planCase{"archived-slash-binding", slash, routing.PlanProposed, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, b, a := approvedProjectionFixtureForPlan(t, tc.path, tc.status)
			js, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(js.Path(b.DeliveryID))
			if err != nil {
				t.Fatal(err)
			}
			view, err := ProjectApprovedMission(root, b, a)
			if err != nil {
				t.Fatal(err)
			}
			if !view.Available || view.Source != domain.SourceCurrent || view.Delivery != b.DeliveryID {
				t.Fatal(view)
			}
			if len(view.Plans) != 1 {
				t.Fatalf("plans = %v, want exactly one", view.Plans)
			}
			want := domain.PlanRef{Name: "alpha", Status: string(tc.status), Done: tc.done}
			if view.Plans[0] != want {
				t.Fatalf("plan = %+v, want %+v", view.Plans[0], want)
			}
			after, err := os.ReadFile(js.Path(b.DeliveryID))
			if err != nil || string(before) != string(after) {
				t.Fatal("projection mutated native journal", err)
			}
		})
	}
}

func rewriteProjectionOpening(t *testing.T, root string, b domain.MissionBinding, change func(*journal.Record)) domain.MissionBinding {
	t.Helper()
	js, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := js.Read(b.DeliveryID)
	if err != nil || len(records) != 1 {
		t.Fatalf("opening fixture: %v, %v", records, err)
	}
	change(&records[0])
	if err := os.Truncate(js.Path(b.DeliveryID), 0); err != nil {
		t.Fatal(err)
	}
	opened, err := js.Append(b.DeliveryID, records[0])
	if err != nil {
		t.Fatal(err)
	}
	b.OpenedDigest = opened.Digest
	return b
}

func supervisedProjectionFixture(t *testing.T) (string, domain.MissionBinding, domain.PlanApproval) {
	t.Helper()
	root, b, a := approvedProjectionFixture(t)
	b = rewriteProjectionOpening(t, root, b, func(record *journal.Record) {
		var detail map[string]json.RawMessage
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		detail["supervision"] = json.RawMessage(`true`)
		detail["head"] = json.RawMessage(`"` + strings.Repeat("a", 40) + `"`)
		var err error
		record.Detail, err = json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
	})
	return root, b, a
}

func appendProjectionTerminal(t *testing.T, root string, b domain.MissionBinding) {
	t.Helper()
	js, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindTerminal,
		Detail: json.RawMessage(`{"state":"done","final_commit":"` + strings.Repeat("b", 40) + `"}`),
		Graph:  json.RawMessage(`{"tasks":[],"waves":[]}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestApprovedSnapshotKeepsObservationAcrossPublication(t *testing.T) {
	root, b, a := supervisedProjectionFixture(t)
	ctx := context.Background()
	p, snapshot, err := approvedDeliverySnapshot(ctx, root, b, a)
	if err != nil {
		t.Fatal(err)
	}
	before, err := projectApprovedSnapshot(ctx, root, b, p, snapshot)
	if err != nil || before.Progression == nil || !before.Progression.Required || before.Progression.Cleared || before.Progression.ReviewID != "" {
		t.Fatalf("before publication: %+v, %v", before, err)
	}
	appendProjectionTerminal(t, root, b)
	captured, err := projectApprovedSnapshot(ctx, root, b, p, snapshot)
	if err != nil || !reflect.DeepEqual(captured.Progression, before.Progression) || captured.Header["state"] != before.Header["state"] {
		t.Fatalf("mixed observations: captured=%+v gate=%+v, prior gate=%+v, err=%v", captured.Header, captured.Progression, before.Progression, err)
	}
	fresh, err := ProjectApprovedMission(root, b, a)
	if err != nil || fresh.Progression == nil || !fresh.Progression.Required || fresh.Progression.Cleared || fresh.Progression.ReviewID == "" || fresh.Header["state"] != loop.StateReviewBlocked {
		t.Fatalf("fresh observation: header=%+v gate=%+v, %v", fresh.Header, fresh.Progression, err)
	}
	if len(snapshot.Records()) != 1 || snapshot.Records()[0].Digest != b.OpenedDigest {
		t.Fatal("publication changed the approved observation")
	}
}

func TestApprovedSnapshotDoesNotReopenJournal(t *testing.T) {
	root, b, a := supervisedProjectionFixture(t)
	ctx := context.Background()
	p, snapshot, err := approvedDeliverySnapshot(ctx, root, b, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, journal.Dir, b.DeliveryID+".jsonl")); err != nil {
		t.Fatal(err)
	}
	view, err := projectApprovedSnapshot(ctx, root, b, p, snapshot)
	if err != nil || !view.Available || view.Progression == nil || !view.Progression.Required || view.Progression.Cleared {
		t.Fatalf("captured projection reopened journal: %+v, %v", view, err)
	}
	if _, err := ProjectApprovedMission(root, b, a); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("fresh projection ignored missing journal: %v", err)
	}
}

func TestApprovedSnapshotRevalidatesCurrentReviewEvidence(t *testing.T) {
	root, b, a := supervisedProjectionFixture(t)
	appendProjectionTerminal(t, root, b)
	ctx := context.Background()
	p, snapshot, err := approvedDeliverySnapshot(ctx, root, b, a)
	if err != nil {
		t.Fatal(err)
	}
	view, err := projectApprovedSnapshot(ctx, root, b, p, snapshot)
	if err != nil || view.Progression == nil || view.Progression.ReviewID == "" || view.Progression.Cleared {
		t.Fatalf("pending review: %+v, %v", view.Progression, err)
	}
	path := filepath.Join(root, ".batuta", "reviews", "supervision", view.Progression.ReviewID, "job.json")
	if err := os.WriteFile(path, []byte("corrupt current review evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	view, err = projectApprovedSnapshot(ctx, root, b, p, snapshot)
	if !errors.Is(err, ErrArtifactIO) || view.Source != domain.SourceUnavailable || view.Available {
		t.Fatalf("captured journal hid corrupt current review: %+v, %v", view, err)
	}
}

func TestApprovedObservationRejectsRechainedForeignIdentity(t *testing.T) {
	for _, field := range []string{"slug", "plan_path", "plan_digest", "workspace", "approval_receipt_id", "plan_content_digest"} {
		t.Run(field, func(t *testing.T) {
			root, b, a := approvedProjectionFixture(t)
			b = rewriteProjectionOpening(t, root, b, func(record *journal.Record) {
				var detail map[string]string
				if err := json.Unmarshal(record.Detail, &detail); err != nil {
					t.Fatal(err)
				}
				detail[field] = "foreign"
				var err error
				record.Detail, err = json.Marshal(detail)
				if err != nil {
					t.Fatal(err)
				}
			})
			if _, err := ValidateApprovedDelivery(root, b, a); !errors.Is(err, ErrArtifactInvalid) {
				t.Fatalf("approved foreign %s: %v", field, err)
			}
			if view, err := ProjectApprovedMission(root, b, a); !errors.Is(err, ErrArtifactInvalid) || view.Available {
				t.Fatalf("projected foreign %s: %+v, %v", field, view, err)
			}
		})
	}
}
