//go:build darwin || linux || windows

package service

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

// Clone before either delivery creates worktrees; both missions retain the same
// qualified CLI/account and the same Store and process manager.
func nativeStopCloneMission(t *testing.T, svc *Service, first domain.PlanApproval) domain.PlanApproval {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sibling")
	for _, args := range [][]string{{"clone", "--local", "--no-hardlinks", first.Identity.Binding.CanonicalRoot, root}, {"-C", root, "config", "user.name", "console-native-qa"}, {"-C", root, "config", "user.email", "qa@example.invalid"}, {"-C", root, "config", "commit.gpgsign", "false"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("local clone: %v %s", err, out)
		}
	}
	m, err := svc.Store.GetMission(first.Identity.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	m.ID, m.WorkspaceID, m.Binding = "stop-sibling", "stop-sibling-workspace", nil
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: m.WorkspaceID, Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(m); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission(m.ID, MissionBindingRequest{WorkspaceID: m.WorkspaceID, PlanPath: first.Identity.Binding.PlanPath}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.PlanDetail(context.Background(), m.ID, m.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == first.ID || a.Identity.Binding.CanonicalRoot == first.Identity.Binding.CanonicalRoot || domain.ExecutionDelivery(a.ID, a.Identity) == domain.ExecutionDelivery(first.ID, first.Identity) {
		t.Fatal("missions did not receive distinct authority", first, a)
	}
	return a
}

func nativeStopAwait(t *testing.T, ctx context.Context, label string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", label, ctx.Err())
		case <-ticker.C:
		}
	}
}

func nativeStopStart(t *testing.T, ctx context.Context, svc *Service, a domain.PlanApproval) domain.ExecutionResult {
	t.Helper()
	r, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
	if err != nil || r.Effect != domain.ExecutionStarted || !domain.ValidExecutionRunID(r.RunID) || r.OpenedDigest == "" {
		t.Fatal("actual public start", r, err)
	}
	return r
}

func nativeStopReference(a domain.PlanApproval, r domain.ExecutionResult) domain.ExecutionStopRequest {
	return domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: r.Delivery, RunID: r.RunID}
}

func nativeStopFinal(t *testing.T, ctx context.Context, svc *Service, req domain.ExecutionStopRequest) domain.ExecutionStopResult {
	t.Helper()
	var result domain.ExecutionStopResult
	nativeStopAwait(t, ctx, "public Stop finalization", func() bool {
		var err error
		result, err = svc.StopExecution(ctx, req)
		if err != nil {
			t.Fatal("fresh Stop caller", result, err)
		}
		return result.Finalized
	})
	if result.ExecutionStopRequest != req || result.Effect == domain.ExecutionStopPending || result.Effect == domain.ExecutionStopRejected {
		t.Fatal("wrong finalization reference", result)
	}
	if result.Effect == domain.ExecutionStopCompleted {
		if !result.Durable || result.Error != nil || result.State == nil || result.State.Active || result.State.RunID != "" || result.State.Watch == nil || result.State.Watch.Header["state"] != result.CoreState {
			t.Fatal("unverified completion", result)
		}
	} else if result.Effect != domain.ExecutionStopUncertain || result.Error == nil || len(result.Causes) == 0 {
		t.Fatal("uncertainty lost causes", result)
	}
	return result
}

func nativeStopCanceledFinal(t *testing.T, ctx context.Context, svc *Service, req domain.ExecutionStopRequest) domain.ExecutionStopResult {
	t.Helper()
	result := nativeStopFinal(t, ctx, svc, req)
	if result.Effect != domain.ExecutionStopCompleted || result.CoreState != loop.StateCanceled || !result.Durable {
		t.Fatalf("actual canceled delivery did not complete durably: %+v causes=%v", result, result.Causes)
	}
	return result
}

func nativeStopReviewed(t *testing.T, ctx context.Context, svc *Service, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	// Like waitNativeIdle, observe the job through review and resource finalization.
	// A public projection during review can fail while Core owns the review gate.
	nativeStopAwait(t, ctx, "sibling actual completion", func() bool {
		svc.native.mu.Lock()
		_, active := svc.native.jobs[r.Delivery]
		svc.native.mu.Unlock()
		return !active
	})
	state, err := svc.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
	if err != nil {
		t.Fatal("sibling final public state", err)
	}
	if state.Active || state.RunID != "" || state.Watch == nil || state.Watch.Header["state"] != loop.StateDone {
		t.Fatal("sibling did not finish implementation", state)
	}
	opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: r.Delivery}
	observation, err := loop.ObserveSupervision(opts)
	if err != nil || observation.Review == nil || observation.Review.State != "reported" || observation.Review.Outcome != "SHIP" || observation.Review.ApprovalReceiptID != a.ID {
		t.Fatal("sibling actual review", observation, err)
	}
	gate, err := loop.CheckSupervisionGate(ctx, opts)
	if err != nil || !gate.Required || !gate.Cleared || gate.EvidenceDigest == "" {
		t.Fatal("sibling review gate", gate, err)
	}
}
