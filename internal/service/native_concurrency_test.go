//go:build darwin || linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeConcurrentStartsSubmitOneApprovedDelivery(t *testing.T) {
	svc, approval, _ := nativeRunFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan nativeStartOutcome, 8)
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			result, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
			results <- nativeStartOutcome{result: result, err: err}
		}()
	}
	close(start)
	started := 0
	for i := 0; i < cap(results); i++ {
		select {
		case outcome := <-results:
			if outcome.err == nil {
				if outcome.result.Effect != domain.ExecutionStarted {
					t.Fatal(outcome)
				}
				started++
			} else if outcome.result.Effect != domain.ExecutionRejected {
				t.Fatal("duplicate start was not definitively rejected", outcome)
			}
		case <-ctx.Done():
			t.Fatal("concurrent starts did not settle")
		}
	}
	if started != 1 {
		t.Fatal("native start count", started)
	}
	waitNativeQuestionAndIdle(t, ctx, svc, approval, domain.ExecutionDelivery(approval.ID, approval.Identity), 0)
	account, _ := adapters.Get("codex")
	launches, err := os.ReadFile(filepath.Join(account.IsolateDir(svc.Home, "acct_native"), "native-launches"))
	if err != nil || string(launches) != "submission\n" {
		t.Fatal("concurrent start duplicated model submission", string(launches), err)
	}
}
