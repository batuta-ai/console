package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func approvalFixture(t *testing.T) (*Service, string) {
	t.Helper()
	svc := setupService(t)
	root := t.TempDir()
	path := serviceBindingFixture(t, root, "alpha", "")
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m", "other"} {
		if err := svc.Store.PutMission(domain.Mission{ID: id, WorkspaceID: "w", Status: "open", CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.BindMission(id, MissionBindingRequest{WorkspaceID: "w", PlanPath: path}); err != nil {
			t.Fatal(err)
		}
	}
	return svc, filepath.Join(root, path)
}
func TestPlanApprovalExactVersions(t *testing.T) {
	svc, path := approvalFixture(t)
	ctx := context.Background()
	shown, err := svc.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	if shown.Approval != nil {
		t.Fatal("native approved header invented receipt")
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shown.Content, original) || shown.Identity.Version != domain.PlanVersion(original) {
		t.Fatal("not full version")
	}
	receipt, err := svc.ApprovePlan(ctx, shown.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receipt.Content, original) || receipt.Identity != shown.Identity {
		t.Fatal("wrong snapshot")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("source rewritten")
	}
	foreign, err := svc.PlanDetail(ctx, "other", "w")
	if err != nil || foreign.Approval != nil {
		t.Fatal("approval leaked across missions", err)
	}
	edited := append(append([]byte{}, original...), []byte("\n## Decisions and context\nNew prose.\n")...)
	if err := os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, shown.Identity); !errors.Is(err, domain.ErrPlanStale) {
		t.Fatalf("stale: %v", err)
	}
	next, err := svc.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	if next.Identity.Version == shown.Identity.Version || next.Identity.Binding.PlanDigest != shown.Identity.Binding.PlanDigest || next.Approval != nil {
		t.Fatal("incorrect edited version")
	}
	second, err := svc.ApprovePlan(ctx, next.Identity)
	if err != nil || second.ID == receipt.ID {
		t.Fatal("distinct approval", err)
	}
	historical, err := svc.PlanApproval(ctx, "m", "w", receipt.ID)
	if err != nil || !bytes.Equal(historical.Content, original) {
		t.Fatal("historical bytes lost", err)
	}
}
func TestPlanApprovalRejectsIdentityAndTransitions(t *testing.T) {
	svc, path := approvalFixture(t)
	ctx := context.Background()
	shown, err := svc.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.PlanIdentity){
		func(p *domain.PlanIdentity) { p.MissionID = "missing" },
		func(p *domain.PlanIdentity) { p.Binding.WorkspaceID = "foreign" },
		func(p *domain.PlanIdentity) { p.Binding.WorkspaceID = p.Binding.WorkspaceRoot },
		func(p *domain.PlanIdentity) { p.Binding.CanonicalRoot = "/foreign" },
		func(p *domain.PlanIdentity) { p.Binding.PlanPath = ".batuta/plans/foreign.md" },
		func(p *domain.PlanIdentity) { p.Binding.PlanSlug = "foreign" },
		func(p *domain.PlanIdentity) { p.Version = "" },
		func(p *domain.PlanIdentity) { p.Version = "invalid" },
		func(p *domain.PlanIdentity) { p.Binding = domain.MissionBinding{} },
	} {
		id := shown.Identity
		change(&id)
		if _, err := svc.ApprovePlan(ctx, id); err == nil {
			t.Fatalf("accepted %+v", id)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.ApprovePlan(canceled, shown.Identity); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled %v", err)
	}
	for _, status := range []string{"in progress", "done"} {
		body := bytes.Replace(shown.Content, []byte("approved"), []byte(status), 1)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		detail, err := svc.PlanDetail(ctx, "m", "w")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApprovePlan(ctx, detail.Identity); !errors.Is(err, domain.ErrPlanTransition) {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApprovePlan(ctx, shown.Identity); err == nil {
		t.Fatal("missing approved")
	}
}

func TestPlanDetailBindingFailures(t *testing.T) {
	for _, mode := range []string{"unassigned", "foreign workspace", "changed root", "changed canonical", "task set", "archived", "started delivery"} {
		t.Run(mode, func(t *testing.T) {
			svc, path := approvalFixture(t)
			ctx := context.Background()
			shown, err := svc.PlanDetail(ctx, "m", "w")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unassigned":
				if err := svc.Store.PutMission(domain.Mission{ID: "unassigned", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.PlanDetail(ctx, "unassigned", "w"); err == nil {
					t.Fatal("unassigned read")
				}
				return
			case "foreign workspace":
				if _, err := svc.PlanDetail(ctx, "m", filepath.Dir(path)); err == nil {
					t.Fatal("root authorized")
				}
				return
			case "changed root":
				if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: t.TempDir(), CreatedAt: domain.Now()}); err != nil {
					t.Fatal(err)
				}
			case "changed canonical":
				b := shown.Identity.Binding
				b.CanonicalRoot = t.TempDir()
				if err := svc.Store.BindMission("m", b); err != nil {
					t.Fatal(err)
				}
			case "task set":
				if err := os.WriteFile(path, bytes.Replace(shown.Content, []byte("verified"), []byte("changed acceptance"), 1), 0600); err != nil {
					t.Fatal(err)
				}
			case "archived":
				archived := filepath.Join(filepath.Dir(path), "done", "alpha.md")
				if err := os.MkdirAll(filepath.Dir(archived), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, archived); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: ".batuta/plans/done/alpha.md"}); err != nil {
					t.Fatal(err)
				}
				detail, err := svc.PlanDetail(ctx, "m", "w")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.ApprovePlan(ctx, detail.Identity); !errors.Is(err, domain.ErrPlanTransition) {
					t.Fatal("archived approval", err)
				}
				return
			case "started delivery":
				serviceBindingFixture(t, shown.Identity.Binding.WorkspaceRoot, "alpha", "run")
				if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: shown.Identity.Binding.PlanPath, DeliveryID: "run"}); err != nil {
					t.Fatal(err)
				}
				detail, err := svc.PlanDetail(ctx, "m", "w")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.ApprovePlan(ctx, detail.Identity); !errors.Is(err, domain.ErrPlanTransition) {
					t.Fatal("started approval", err)
				}
				return
			}
			if _, err := svc.PlanDetail(ctx, "m", "w"); err == nil {
				t.Fatal("binding changed but detail accepted")
			}
			if _, err := svc.ApprovePlan(ctx, shown.Identity); err == nil {
				t.Fatal("binding changed but approval accepted")
			}
		})
	}
}
