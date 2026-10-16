package service

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"modernc.org/sqlite"
)

func TestStopSessionAfterAdmissionFreeze(t *testing.T) {
	svc, _ := persistenceSvc(t)
	t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
	live, err := svc.Session.Start(domain.Session{ID: "frozen-admission", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSession(live); err != nil {
		t.Fatal(err)
	}
	svc.Session.CloseAdmission()
	if _, err := svc.Session.Start(domain.Session{ID: "denied", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, ""); err == nil || err.Error() != "session manager is shutting down" {
		t.Fatalf("frozen admission accepted new start: %v", err)
	}
	stopped, err := svc.StopSession(live.ID)
	if err != nil || stopped.Process != "exited" {
		t.Fatalf("admission freeze blocked owned stop: %+v %v", stopped, err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 2 || events[0].Kind != "session.exited" || events[1].Kind != "session.stopped" {
		t.Fatalf("durable stop: %+v %v", events, err)
	}
}

func TestStopSessionCatalogFailureDuringDrain(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	name := fmt.Sprintf("stop_catalog_%d", stopSQLBarrierID.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, db := persistenceSvc(t)
	t.Cleanup(func() { unblock(); svc.Session.Shutdown(context.Background()) })
	if err := svc.Store.PutSession(domain.Session{ID: "historical-drain", Process: "running", PID: -1}); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, `CREATE TRIGGER reject_recovery_drain BEFORE INSERT ON events WHEN NEW.kind='session.reconciled' BEGIN SELECT `+name+`(); SELECT RAISE(ABORT,'catalog drain write rejected'); END`)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan stopOutcome, 1)
	go func() { row, err := svc.StopSessionContext(ctx, "historical-drain"); done <- stopOutcome{row, err} }()
	stopAwait(t, entered)
	cause := errors.New("catalog caller left")
	cancel(cause)
	got := stopAwait(t, done)
	var pending *session.FinalizationPendingError
	if !errors.Is(got.err, cause) || !errors.As(got.err, &pending) {
		t.Fatalf("pending catalog write: %+v %v", got.row, got.err)
	}
	if err := svc.Session.Shutdown(ctx); !errors.Is(err, cause) {
		t.Fatalf("held catalog drain: %v", err)
	}
	unblock()
	drainErr := svc.Session.Shutdown(context.Background())
	var persistenceErr *session.StopPersistenceError
	if !errors.As(drainErr, &persistenceErr) || !strings.Contains(drainErr.Error(), "catalog drain write rejected") {
		t.Fatalf("catalog-only drain lost persistence failure: %v", drainErr)
	}
	if _, err := svc.StopSessionContext(ctx, "historical-drain"); !errors.As(err, &persistenceErr) || errors.As(err, &pending) {
		t.Fatalf("completed retry lost failure: %v", err)
	}
	row, err := svc.Store.GetSession("historical-drain")
	if err != nil || row.Process != "running" {
		t.Fatalf("failed recovery changed row: %+v %v", row, err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed recovery committed events: %+v %v", events, err)
	}
}
