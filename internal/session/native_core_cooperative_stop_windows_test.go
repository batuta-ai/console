package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

func TestNativeCoreServiceContractCancellationWithoutJournalHold(t *testing.T) {
	image := contractImage(t)
	f := newContractFixture(t, image)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started, observer := f.start(t, ctx)
	req := domain.ExecutionStopRequest{Target: f.approval.Identity, ApprovalID: f.approval.ID, Delivery: started.Delivery, RunID: started.RunID}
	pending, err := f.svc.StopExecution(ctx, req)
	if err != nil || pending.Finalized || pending.Durable || pending.Effect != domain.ExecutionStopPending {
		t.Fatal("public Stop acceptance", pending, err)
	}
	var final domain.ExecutionStopResult
	contractAwait(t, ctx, "public cooperative cancellation without journal hold", func() bool {
		var err error
		final, err = f.svc.StopExecution(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return final.Finalized
	})
	owner := contractJoinedOwner(t, observer)
	if final.ExecutionStopRequest != req || final.Effect != domain.ExecutionStopCompleted || !final.Durable || final.Error != nil || final.CoreState != loop.StateCanceled || final.State == nil || final.State.Active || final.State.RunID != "" || final.State.Watch == nil || final.State.Watch.Header["state"] != loop.StateCanceled || owner.Forced || owner.ExitCode != 0 {
		t.Fatal("cancellation did not complete cooperatively", final, owner)
	}
	binding := f.approval.Identity.Binding
	binding.DeliveryID, binding.OpenedDigest = started.Delivery, started.OpenedDigest
	evidence, err := batuta.ObserveApprovedExecutionContext(ctx, binding.CanonicalRoot, binding, f.approval)
	if err != nil || evidence.TerminalState != loop.StateCanceled || evidence.TerminalDigest == "" || evidence.CleanupPending {
		t.Fatal("canceled synchronized terminal", evidence, err)
	}
	t.Log("cooperative Stop without journal hold: durable canceled terminal and joined owner without force")
}
