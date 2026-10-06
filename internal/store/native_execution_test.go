package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestApprovedExecutionAttachmentRejectsReassignedBinding(t *testing.T) {
	s, _, identity, content := approvalStoreFixture(t)
	a, err := s.ApprovePlan(context.Background(), identity, func() ([]byte, error) { return content, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := identity.Binding
	target.DeliveryID, target.OpenedDigest = "native-one", "native-opening"
	replacement := identity.Binding
	replacement.PlanPath = ".batuta/plans/replacement.md"
	replacement.PlanSlug = "replacement"
	replacement.PlanDigest = "replacement-digest"
	if err := s.BindMission(identity.MissionID, replacement); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachApprovedExecution(context.Background(), a.ID, identity, target); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatal("native attach reverted reassignment", err)
	}
	m, err := s.GetMission(identity.MissionID)
	if err != nil || m.Binding == nil || *m.Binding != replacement {
		t.Fatal("rejected attachment mutated mission", m, err)
	}
}

func TestApprovedExecutionAttachmentIsAtomicIdempotentAndScoped(t *testing.T) {
	s, _, identity, content := approvalStoreFixture(t)
	a, err := s.ApprovePlan(context.Background(), identity, func() ([]byte, error) { return content, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := identity.Binding
	target.DeliveryID, target.OpenedDigest = "native-one", "native-opening"
	if err := s.AttachApprovedExecution(context.Background(), "foreign", identity, target); err == nil {
		t.Fatal("unknown receipt accepted")
	}
	execSQL(t, s.db, `CREATE TRIGGER reject_native_event BEFORE INSERT ON events WHEN NEW.kind='execution.bound' BEGIN SELECT RAISE(ABORT,'event refused'); END`)
	if err := s.AttachApprovedExecution(context.Background(), a.ID, identity, target); err == nil {
		t.Fatal("event failure accepted")
	}
	m, err := s.GetMission(identity.MissionID)
	if err != nil || m.Binding == nil || *m.Binding != identity.Binding {
		t.Fatal("event failure changed binding", m, err)
	}
	execSQL(t, s.db, `DROP TRIGGER reject_native_event`)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.AttachApprovedExecution(context.Background(), a.ID, identity, target)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM events WHERE kind='execution.bound'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("attachment duplicated durable event", count, err)
	}
	replacement := target
	replacement.DeliveryID = "native-two"
	if err := s.AttachApprovedExecution(context.Background(), a.ID, identity, replacement); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatal("existing execution was replaced", err)
	}
}
