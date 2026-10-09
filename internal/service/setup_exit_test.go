package service

import (
	"fmt"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestSetupConcurrentOwnedExitNeverInterrupted(t *testing.T) {
	svc, _ := persistenceSvc(t)
	w := domain.Workspace{ID: "setup-exit", Root: t.TempDir()}
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
	const attempts = 100
	for n := range attempts {
		row, err := svc.Session.Start(domain.Session{
			ID: fmt.Sprintf("setup-exit-%03d", n), WorkspaceID: w.ID,
			AccountID: "selected", Work: "init", Argv: testfixture.Argv(t, []string{"/bin/cat"}), CreatedAt: domain.Now(),
		}, nil, w.Root)
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
				got, err := svc.DecorateWorkspaceChecked(w)
				if err != nil {
					t.Error(err)
					return
				}
				a := got.Setup.Attempt
				if a == nil || a.SessionID != row.ID || a.AccountID != row.AccountID ||
					(a.Process != "running" && a.Process != "exited") || a.RecoveryReason != "" || a.StopRequested ||
					(a.State != "running" && a.State != "exited_incomplete") ||
					(got.Setup.State != "running" && got.Setup.State != "partial") || got.Setup.Ready {
					t.Errorf("owned init exit misreported: setup=%+v attempt=%+v", got.Setup, a)
				}
			}()
		}
		operations.Add(1)
		go func() {
			defer operations.Done()
			<-gate
			// Manager stop produces a native exit without a service stop-request event.
			if _, err := svc.Session.Stop(row.ID); err != nil {
				t.Error(err)
			}
		}()
		close(gate)
		operations.Wait()
		stored, err := svc.Store.GetSession(row.ID)
		if err != nil || stored.Process != "exited" || stored.EndedAt == nil || stored.RecoveryReason != "" {
			t.Fatalf("completed owned exit: %+v %v", stored, err)
		}
		got, err := svc.DecorateWorkspaceChecked(w)
		if err != nil || got.Setup.Attempt == nil || got.Setup.Attempt.EndedAt == nil ||
			!got.Setup.Attempt.EndedAt.Equal(*stored.EndedAt) || got.Setup.Attempt.State != "exited_incomplete" {
			t.Fatalf("terminal attempt lost: %+v %v", got.Setup.Attempt, err)
		}
	}
	events, err := svc.Store.EventsSince(0, attempts+1)
	if err != nil || len(events) != attempts {
		t.Fatalf("expected only native exit events: events=%d error=%v", len(events), err)
	}
	for _, event := range events {
		if event.Kind != "session.exited" {
			t.Errorf("DTO read changed lifecycle evidence: %+v", event)
		}
	}
}
