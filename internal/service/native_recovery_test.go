//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

func TestNativeRestartKeepsDeliveryAndRejectsForeignResumeBeforeSubmission(t *testing.T) {
	svc, a, root := nativeRunFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	question := waitNativeQuestionAndIdle(t, ctx, svc, a, started.Delivery, 0)
	if err := svc.ShutdownNative(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Session.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(svc.Home, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(st, session.New(t.TempDir()), nil)
	restarted.Home = svc.Home
	restarted.CoreExecutable = svc.CoreExecutable
	defer func() {
		drain, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		nativeErr := restarted.ShutdownNative(drain)
		sessionErr := restarted.Session.Shutdown(drain)
		if nativeErr != nil || sessionErr != nil {
			t.Error("restart fixture drain", nativeErr, sessionErr)
			return
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	account, err := st.GetAccount("acct_native")
	if err != nil {
		t.Fatal(err)
	}
	authenticateFixtureAccount(t, restarted, account, "native-account")
	if err := os.Remove(filepath.Join(root, a.Identity.Binding.PlanPath)); err != nil {
		t.Fatal(err)
	}
	detail, err := restarted.PlanDetail(ctx, "m", "w")
	if err != nil || detail.Approval == nil || detail.Approval.ID != a.ID || detail.Identity != a.Identity || string(detail.Content) != string(a.Content) {
		t.Fatal("restart could not recover the approved snapshot from the bound delivery", detail, err)
	}
	state, err := restarted.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || !state.Bound || state.Active || state.Delivery != started.Delivery || state.OpenedDigest != started.OpenedDigest || state.Watch == nil || state.Watch.PendingQuestion == nil {
		t.Fatal("restart lost native authority", state, err)
	}
	if _, err := restarted.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}); !errors.Is(err, domain.ErrExecutionExists) {
		t.Fatal("restart duplicated native start", err)
	}
	for _, kind := range []string{"approval", "delivery", "opening", "mission", "version"} {
		req := domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: started.Delivery, OpenedDigest: started.OpenedDigest}
		switch kind {
		case "approval":
			req.ApprovalID = "foreign"
		case "delivery":
			req.Delivery = "foreign"
		case "opening":
			req.OpenedDigest = "sha256:" + strings.Repeat("a", 64)
		case "mission":
			req.Target.MissionID = "foreign"
		case "version":
			req.Target.Version = domain.PlanVersion([]byte("foreign"))
		}
		if result, err := restarted.ResumeExecution(ctx, req); err == nil || result.Effect != domain.ExecutionRejected {
			t.Fatal("foreign native resume accepted", kind, result, err)
		}
	}
	answered, err := restarted.AnswerQuestion(ctx, question.Target.Answer("approved restart answer"))
	if err != nil || answered.Delivery != started.Delivery {
		t.Fatal(answered, err)
	}
	resumed, err := restarted.ResumeExecution(ctx, domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: started.Delivery, OpenedDigest: started.OpenedDigest})
	if err != nil || resumed.Delivery != started.Delivery {
		t.Fatal(resumed, err)
	}
	waitNativeQuestionAndIdle(t, ctx, restarted, a, started.Delivery, question.Target.Execution)
}
