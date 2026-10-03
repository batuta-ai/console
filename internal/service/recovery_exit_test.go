package service

import (
	"fmt"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestRecoveryConcurrentOwnedExitNeverBecomesLost(t *testing.T) {
	svc, _ := persistenceSvc(t)
	finish := make(chan struct{})
	var readers sync.WaitGroup
	for range 64 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-finish:
					return
				default:
					svc.Session.Live("unrelated")
				}
			}
		}()
	}
	defer func() { close(finish); readers.Wait() }()
	for n := range 100 {
		row, err := svc.Session.Start(domain.Session{ID: fmt.Sprintf("owned-exit-%d", n), Argv: []string{"/bin/cat"}, CreatedAt: domain.Now()}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Store.PutSession(row); err != nil {
			svc.Session.Stop(row.ID)
			t.Fatal(err)
		}
		gate := make(chan struct{})
		var operations sync.WaitGroup
		for range 8 {
			operations.Add(1)
			go func() {
				defer operations.Done()
				<-gate
				got, err := svc.ReconcileSession(row)
				if err != nil || (got.Process != "running" && got.Process != "exited") || got.RecoveryReason != "" {
					t.Errorf("owned lifecycle was recovered as lost: %+v %v", got, err)
				}
			}()
		}
		operations.Add(1)
		go func() {
			defer operations.Done()
			<-gate
			if _, err := svc.Session.Stop(row.ID); err != nil {
				t.Error(err)
			}
		}()
		close(gate)
		operations.Wait()
		stored, err := svc.Store.GetSession(row.ID)
		if err != nil || stored.Process != "exited" || stored.EndedAt == nil || stored.RecoveryReason != "" {
			t.Fatalf("completed owned stop: %+v %v", stored, err)
		}
	}
	events, err := svc.Store.EventsSince(0, 1000)
	if err != nil || len(events) != 100 {
		t.Fatalf("owned lifecycles must each emit only their exit: events=%d error=%v", len(events), err)
	}
	for _, event := range events {
		if event.Kind != "session.exited" {
			t.Errorf("false recovery event for owned lifecycle: %+v", event)
		}
	}
}
