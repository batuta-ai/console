//go:build darwin || linux

package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestNativeStopCatalogWaitDoesNotOwnAdmissionLock(t *testing.T) {
	svc, approval, _ := nativeStopTerminalFixture(t)
	other, err := svc.PlanDetail(context.Background(), "other", "w")
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.native.admitExecution(domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID}, domain.ExecutionDelivery(approval.ID, approval.Identity))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	entered, release := make(chan struct{}), make(chan struct{})
	storeDone := make(chan error, 1)
	go func() {
		_, err := svc.Store.ApprovePlan(context.Background(), other.Identity, func() ([]byte, error) {
			close(entered)
			<-release
			return other.Content, nil
		})
		storeDone <- err
	}()
	<-entered
	stopDone := make(chan error, 1)
	go func() {
		_, err := svc.StopExecution(context.Background(), domain.ExecutionStopRequest{Target: approval.Identity, ApprovalID: approval.ID, Delivery: job.delivery, RunID: job.runID})
		stopDone <- err
	}()
	defer func() {
		close(release)
		if err := <-storeDone; err != nil {
			t.Error(err)
		}
		if err := <-stopDone; err != nil {
			t.Error(err)
		}
	}()
	// The real Store owns its sole connection until the read callback releases.
	// Observe Stop in that connection wait before testing mutex ownership.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		blocked := false
		for _, stack := range strings.Split(string(buf[:n]), "\n\ngoroutine ") {
			if strings.Contains(stack, ".(*Service).StopExecution") && strings.Contains(stack, "database/sql.(*DB).conn") {
				blocked = true
				break
			}
		}
		if blocked {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("Stop never entered the controlled catalog wait")
		}
		runtime.Gosched()
	}
	if !svc.native.mu.TryLock() {
		t.Error("catalog validation holds the native admission lock")
	} else {
		svc.native.mu.Unlock()
	}
}

func TestNativeStopRequiresMatchingDurableTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, terminal, returned, projected string
	}{
		{"missing_terminal", "", loop.StateCanceled, "running"},
		{"mismatched_terminal", loop.StateBlocked, loop.StateCanceled, loop.StateBlocked},
		{"unresolved_review", loop.StateDone, loop.StateDone, loop.StateReviewBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, approval, binding := nativeStopTerminalFixture(t)
			if tc.terminal != "" {
				js, err := journal.Open(binding.CanonicalRoot)
				if err != nil {
					t.Fatal(err)
				}
				detail, err := json.Marshal(map[string]string{"state": tc.terminal})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := js.Append(binding.DeliveryID, journal.Record{Kind: loop.KindTerminal, Detail: detail, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)}); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			state, err := svc.ExecutionState(ctx, approval.Identity.MissionID, binding.WorkspaceID, approval.ID)
			if err != nil || !state.Bound || state.OpenedDigest == "" || state.Watch == nil || state.Watch.Header["state"] != tc.projected {
				t.Fatalf("qualified opening/projection prerequisite: %+v, %v", state, err)
			}
			job, err := svc.native.admitExecution(domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID}, binding.DeliveryID)
			if err != nil {
				t.Fatal(err)
			}
			stop := domain.ExecutionStopRequest{Target: approval.Identity, ApprovalID: approval.ID, Delivery: binding.DeliveryID, RunID: job.runID}
			pending, err := svc.StopExecution(ctx, stop)
			if err != nil || pending.Effect != domain.ExecutionStopPending {
				svc.native.finish(job)
				t.Fatal(pending, err)
			}
			job.recordRun(tc.returned, context.Canceled)
			svc.finishNativeExecution(job, approval)
			got, err := svc.StopExecution(ctx, stop)
			if err != nil {
				t.Fatal(err)
			}
			if got.Effect != domain.ExecutionStopUncertain || !got.Finalized || got.Error == nil || got.CoreState != tc.returned || len(got.Causes) == 0 {
				t.Fatalf("unverified finalization reported as complete: effect=%s finalized=%v durable=%v core=%q projected=%q causes=%v error=%+v", got.Effect, got.Finalized, got.Durable, got.CoreState, got.State.Watch.Header["state"], got.Causes, got.Error)
			}
			if tc.terminal != loop.StateDone && got.Durable {
				t.Fatal("missing or contradictory terminal reported durable")
			}
		})
	}
}

func nativeStopTerminalFixture(t *testing.T) (*Service, domain.PlanApproval, domain.MissionBinding) {
	t.Helper()
	svc, _ := approvalFixture(t)
	ctx := context.Background()
	detail, err := svc.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	approval, err := svc.ApprovePlan(ctx, detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	binding := approval.Identity.Binding
	binding.DeliveryID = domain.ExecutionDelivery(approval.ID, approval.Identity)
	js, err := journal.Open(binding.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	opening, err := json.Marshal(map[string]any{"slug": binding.PlanSlug, "plan_path": binding.PlanPath, "plan_digest": binding.PlanDigest, "workspace": binding.CanonicalRoot, "approval_receipt_id": approval.ID, "plan_content_digest": approval.Identity.Version, "supervision": true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := js.Append(binding.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: opening, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	binding.OpenedDigest = record.Digest
	if err := svc.Store.BindMission("m", binding); err != nil {
		t.Fatal(err)
	}
	return svc, approval, binding
}

func TestNativeStopExactAdmissionAndPendingLifetime(t *testing.T) {
	svc, a, _ := nativeRunFixture(t)
	req := domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}
	job, err := svc.native.admitExecution(req, domain.ExecutionDelivery(a.ID, a.Identity))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	stop := domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: job.delivery, RunID: job.runID}
	state, err := svc.ExecutionState(context.Background(), "m", "w", a.ID)
	if err != nil || !state.Active || state.RunID != job.runID || state.OpenedDigest != "" {
		t.Fatal(state, err)
	}
	wrong := stop
	wrong.RunID = strings.Repeat("f", 64)
	if _, err := svc.StopExecution(context.Background(), wrong); !errors.Is(err, domain.ErrInvalidReference) || job.ctx.Err() != nil {
		t.Fatal("foreign authority affected job", err)
	}
	out, err := svc.StopExecution(context.Background(), stop)
	if err != nil || out.Effect != domain.ExecutionStopPending || out.Finalized || out.Durable || job.ctx.Err() != context.Canceled {
		t.Fatal(out, err)
	}
	again, err := svc.StopExecution(context.Background(), stop)
	if err != nil || !reflect.DeepEqual(again, out) {
		t.Fatal("repeat pending", again, err)
	}
}
