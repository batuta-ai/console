package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"modernc.org/sqlite"

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

func TestApprovedExecutionAttachmentReservationDoesNotChangeRows(t *testing.T) {
	s, _, identity, content := approvalStoreFixture(t)
	a, err := s.ApprovePlan(t.Context(), identity, func() ([]byte, error) { return content, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := identity.Binding
	target.DeliveryID, target.OpenedDigest = "native-one", "native-opening"
	if err := s.AttachApprovedExecution(t.Context(), a.ID, identity, target); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachApprovedExecution(t.Context(), a.ID, identity, target); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("idempotent reservation changed rows: before=%d after=%d", before, after)
	}
}

func TestApprovedExecutionAttachmentReservationSerializesWriters(t *testing.T) {
	for _, attached := range []bool{false, true} {
		for _, commit := range []bool{false, true} {
			name := "unopened-rollback"
			if attached {
				name = "attached-rollback"
			}
			if commit {
				name += "-commit"
			}
			t.Run(name, func(t *testing.T) {
				s, path, identity, content := approvalStoreFixture(t)
				a, err := s.ApprovePlan(t.Context(), identity, func() ([]byte, error) { return content, nil })
				if err != nil {
					t.Fatal(err)
				}
				target := identity.Binding
				target.DeliveryID, target.OpenedDigest = "native-one", "native-opening"
				if attached {
					if err := s.AttachApprovedExecution(t.Context(), a.ID, identity, target); err != nil {
						t.Fatal(err)
					}
				}
				var mode string
				if err := s.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
					t.Fatal("WAL required", mode, err)
				}
				observer, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := observer.Close(); err != nil {
						t.Error(err)
					}
				})
				if _, err := observer.db.Exec(`PRAGMA busy_timeout=0`); err != nil {
					t.Fatal(err)
				}
				tx, err := s.db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.ExecContext(t.Context(), `UPDATE missions SET plan_digest='inflight-reassignment' WHERE id=?`, identity.MissionID); err != nil {
					t.Fatal(err)
				}
				var busy *sqlite.Error
				if err := observer.AttachApprovedExecution(t.Context(), a.ID, identity, target); !errors.As(err, &busy) || busy.Code()&255 != 5 {
					t.Fatal("attachment bypassed pending writer", err)
				}
				if commit {
					err = tx.Commit()
				} else {
					err = tx.Rollback()
				}
				if err != nil {
					t.Fatal(err)
				}
				err = observer.AttachApprovedExecution(t.Context(), a.ID, identity, target)
				if commit {
					if !errors.Is(err, domain.ErrBindingConflict) {
						t.Fatal("committed reassignment accepted", err)
					}
					mission, readErr := observer.GetMission(identity.MissionID)
					if readErr != nil || mission.Binding == nil || mission.Binding.PlanDigest != "inflight-reassignment" {
						t.Fatal("reassignment reverted", mission, readErr)
					}
				} else if err != nil {
					t.Fatal("rollback prevented valid attachment", err)
				}
			})
		}
	}
}
