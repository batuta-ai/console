package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestTask43ArchivedPlanTransitions(t *testing.T) {
	native := filepath.Join(".batuta", "plans", "done", "alpha.md")
	paths := []string{native}
	if slash := filepath.ToSlash(native); slash != native {
		paths = append(paths, slash)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			svc := setupService(t)
			root := t.TempDir()
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			body := []byte("# Plan — alpha\n**Status:** approved\n- [ ] 1. Work — data/high\n  Accept: verified\n")
			if err := os.WriteFile(full, body, 0600); err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", Status: "open", CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: path}); err != nil {
				t.Fatal(err)
			}
			detail, err := svc.PlanDetail(context.Background(), "m", "w")
			if err != nil || detail.Identity.Binding.PlanPath != path {
				t.Fatalf("exact archived detail: %+v error=%v", detail, err)
			}
			if _, err := svc.ApprovePlan(context.Background(), detail.Identity); !errors.Is(err, domain.ErrPlanTransition) {
				t.Errorf("archived plan became approvable: %v", err)
			}
			// An existing receipt does not make an archived source executable.
			receipt, err := svc.Store.ApprovePlan(context.Background(), detail.Identity, func() ([]byte, error) { return body, nil })
			if err != nil {
				t.Fatal(err)
			}
			req := domain.ExecutionRequest{Target: detail.Identity, ApprovalID: receipt.ID}
			if _, err := svc.executionApproval(context.Background(), req); !errors.Is(err, domain.ErrPlanTransition) {
				t.Fatalf("archived receipt became executable: %v", err)
			}
			b := detail.Identity.Binding
			b.DeliveryID = batuta.ExecutionDelivery(receipt)
			js, err := journal.Open(b.CanonicalRoot)
			if err != nil {
				t.Fatal(err)
			}
			opening, err := json.Marshal(map[string]string{"slug": b.PlanSlug, "plan_path": b.PlanPath, "plan_digest": b.PlanDigest, "workspace": b.CanonicalRoot, "approval_receipt_id": receipt.ID, "plan_content_digest": receipt.Identity.Version})
			if err != nil {
				t.Fatal(err)
			}
			opened, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: opening, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
			if err != nil {
				t.Fatal(err)
			}
			b.OpenedDigest = opened.Digest
			if err := svc.Store.BindMission("m", b); err != nil {
				t.Fatal(err)
			}
			// Qualify rejection before any native runner is available or admitted.
			svc.native = nil
			state, err := svc.ExecutionState(context.Background(), "m", "w", receipt.ID)
			if err != nil || state.Target != detail.Identity || state.OpenedDigest != opened.Digest || !state.Bound {
				t.Fatalf("invalid resume fixture: %+v error=%v", state, err)
			}
			if state.Watch == nil || len(state.Watch.Plans) != 1 || !state.Watch.Plans[0].Done {
				t.Fatalf("native archived plan lost archive status: %+v", state.Watch)
			}
			before, err := os.ReadFile(js.Path(b.DeliveryID))
			if err != nil {
				t.Fatal(err)
			}
			resume := domain.ResumeRequest{Target: detail.Identity, ApprovalID: receipt.ID, Delivery: b.DeliveryID, OpenedDigest: b.OpenedDigest}
			result, err := svc.ResumeExecution(context.Background(), resume)
			if !errors.Is(err, domain.ErrPlanTransition) || result.Effect != domain.ExecutionRejected {
				t.Fatalf("archived source eligible for native resume: %+v error=%v", result, err)
			}
			after, err := os.ReadFile(js.Path(b.DeliveryID))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("archive rejection changed journal", err)
			}
		})
	}
}
