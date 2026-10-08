//go:build windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestTask43WindowsArchivedPlanTransitions(t *testing.T) {
	native := filepath.Join(".batuta", "plans", "done", "alpha.md")
	for _, path := range []string{native, filepath.ToSlash(native)} {
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
		})
	}
}
