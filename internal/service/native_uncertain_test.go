//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestNativePartialOpeningIsUncertainAndNeverResubmitted(t *testing.T) {
	svc, a, _ := nativeRunFixture(t)
	delivery := domain.ExecutionDelivery(a.ID, a.Identity)
	path := filepath.Join(a.Identity.Binding.CanonicalRoot, ".batuta/journal", delivery+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{partial-native-opening"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for range 2 {
		result, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
		if !errors.Is(err, domain.ErrExecutionUncertain) || result.Effect != domain.ExecutionUncertain || result.Delivery != delivery || result.OpenedDigest != "" {
			t.Fatal("partial native start mislabeled as rejected", result, err)
		}
		for {
			svc.native.mu.Lock()
			active := len(svc.native.jobs) != 0
			svc.native.mu.Unlock()
			if !active {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != "{partial-native-opening" {
		t.Fatal("partial native evidence was overwritten", string(bytes), err)
	}
	m, err := svc.Store.GetMission("m")
	if err != nil || m.Binding.DeliveryID != "" {
		t.Fatal("partial opening attached to mission", m, err)
	}
}
