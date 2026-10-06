//go:build darwin || linux

package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

func nativeCompletedReviewFixture(t *testing.T, valid bool) (*Service, domain.PlanApproval) {
	t.Helper()
	helper := nativeHelperFixture(t)
	svc, approval, _ := nativeRunFixture(t)
	svc.NativeExecutorExecutable = helper
	cli, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	report := "TASK 1: DONE\\n"
	if valid {
		report = "<<<FINDINGS\\nFINDINGS>>>\\n" + report
	}
	body := strings.Replace(string(bytes), "--verify) printf 'TASK 1: DONE\\n';exit 0;;", "--verify) printf 'review\\n' >> \"$HOME/native-reviews\";printf '"+report+"';exit 0;;", 1)
	body = strings.Replace(body, "printf 'BATUTA-QUESTION: which approved output?\\n'", "printf 'approved output\\n' > out.txt\nprintf 'completed\\n'", 1)
	if err := os.WriteFile(cli, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return svc, approval
}

func waitNativeIdle(t *testing.T, ctx context.Context, svc *Service, delivery string) {
	t.Helper()
	for {
		svc.native.mu.Lock()
		_, active := svc.native.jobs[delivery]
		svc.native.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native implementation/review did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestNativeCompletedTaskRunsQualifiedReviewBeforeReportingComplete(t *testing.T) {
	svc, approval := nativeCompletedReviewFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
	if err != nil {
		t.Fatal(started, err)
	}
	waitNativeIdle(t, ctx, svc, started.Delivery)
	observer := loop.SupervisionOptions{Workspace: approval.Identity.Binding.CanonicalRoot, Delivery: started.Delivery}
	observation, err := loop.ObserveSupervision(observer)
	if err != nil || observation.Review == nil || observation.Review.State != "reported" || observation.Review.Outcome != "SHIP" {
		t.Fatal("qualified Core review was not completed", observation, err)
	}
	gate, err := loop.CheckSupervisionGate(ctx, observer)
	if err != nil || !gate.Required || !gate.Cleared || gate.EvidenceDigest == "" {
		t.Fatal("Core review gate did not clear", gate, err)
	}
	account, _ := adapters.Get("codex")
	reviews, err := os.ReadFile(filepath.Join(account.IsolateDir(svc.Home, "acct_native"), "native-reviews"))
	if err != nil || !strings.Contains(string(reviews), "review\n") {
		t.Fatal("qualified review did not use the selected account", string(reviews), err)
	}
	state, err := svc.ExecutionState(ctx, "m", "w", approval.ID)
	if err != nil || state.Watch == nil || state.Watch.Header["state"] != loop.StateDone {
		t.Fatal("reviewed native completion missing", state, err)
	}
}

func TestNativeInvalidReviewCannotReportCompletedMission(t *testing.T) {
	svc, approval := nativeCompletedReviewFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
	if err != nil {
		t.Fatal(started, err)
	}
	waitNativeIdle(t, ctx, svc, started.Delivery)
	gate, err := loop.CheckSupervisionGate(ctx, loop.SupervisionOptions{Workspace: approval.Identity.Binding.CanonicalRoot, Delivery: started.Delivery})
	if err != nil || !gate.Required || gate.Cleared {
		t.Fatal("native invalid review did not retain its gate", gate, err)
	}
	state, err := svc.ExecutionState(ctx, "m", "w", approval.ID)
	if err != nil || state.Watch == nil || state.Watch.Header["state"] != loop.StateReviewBlocked {
		t.Fatal("unaccepted native review appeared as completed mission", state, err)
	}
}
