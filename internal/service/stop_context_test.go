package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"modernc.org/sqlite"
)

type stopOutcome struct {
	row domain.Session
	err error
}

func TestStopSessionContextMissingDoesNotRetainFailure(t *testing.T) {
	svc, _ := persistenceSvc(t)
	t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
	if _, err := svc.StopSession("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.StopSessionContext(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("missing lookup retained completed history: %v", err)
	}
}

func stopAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("stop phase watchdog")
	}
	var zero T
	return zero
}

// MarshalJSON runs inside the actual Store transaction, holding its sole
// connection after the row write and before event insertion/commit.
type stopTransactionBarrier struct{ entered, release chan struct{} }

func (b stopTransactionBarrier) MarshalJSON() ([]byte, error) {
	close(b.entered)
	<-b.release
	return []byte(`true`), nil
}

func TestStopSessionContextPersistencePhases(t *testing.T) {
	for _, phase := range []string{"exit", "catalog"} {
		for _, mode := range []string{"cancel", "cause", "deadline"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				svc, _ := persistenceSvc(t)
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(func() { unblock(); svc.Session.Shutdown(context.Background()) })
				var barrierDone chan error
				onExit := svc.Session.OnExit
				svc.Session.OnExit = func(row domain.Session) error {
					if phase == "exit" {
						close(entered)
						<-release
						return onExit(row)
					}
					if err := onExit(row); err != nil {
						return err
					}
					barrierDone = make(chan error, 1)
					go func() {
						barrierDone <- svc.Store.PutWorkspaceWithEvent(domain.Workspace{ID: "barrier"}, "barrier", map[string]any{"hold": stopTransactionBarrier{entered, release}})
					}()
					<-entered
					return nil
				}
				live, err := svc.Session.Start(domain.Session{ID: "phases", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.Store.PutSession(live); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				if mode == "deadline" {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, 2*time.Second)
					defer deadlineCancel()
				}
				legacy := make(chan stopOutcome, 1)
				go func() { row, err := svc.StopSession(live.ID); legacy <- stopOutcome{row, err} }()
				stopAwait(t, entered)
				result := make(chan stopOutcome, 1)
				go func() { row, err := svc.StopSessionContext(ctx, live.ID); result <- stopOutcome{row, err} }()
				cause := error(context.Canceled)
				if mode == "cause" {
					cause = errors.New("foreground caller left")
				}
				if phase == "catalog" {
					// Join the actual exit callback before testing the Service read.
					_, err := svc.Session.Stop(live.ID)
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
				if mode == "deadline" {
					cause = context.DeadlineExceeded
					<-ctx.Done()
				} else {
					cancel(cause)
				}
				got := stopAwait(t, result)
				var pending *session.FinalizationPendingError
				if !errors.Is(got.err, cause) || !errors.As(got.err, &pending) || pending.SessionID != live.ID || got.row.Process != "exited" || got.row.EndedAt == nil {
					t.Fatalf("pending result: %+v %v", got.row, got.err)
				}
				// Observe each join before releasing persistence or starting shutdown.
				retries := make(chan stopOutcome, 8)
				for range 8 {
					joined := make(chan struct{})
					joinCtx := &stopJoinContext{Context: context.Background(), joined: joined}
					go func() { row, err := svc.StopSessionContext(joinCtx, live.ID); retries <- stopOutcome{row, err} }()
					stopAwait(t, joined)
				}
				if err := svc.Session.Shutdown(ctx); !errors.Is(err, cause) {
					t.Fatalf("shutdown abandoned finalizer: %v", err)
				}
				if _, err := svc.StopSessionContext(ctx, live.ID); !errors.As(err, &pending) || !errors.Is(err, cause) {
					t.Fatalf("canceled retry: %v", err)
				}
				select {
				case got := <-retries:
					t.Fatalf("legacy stop returned before commit: %+v", got)
				default:
				}
				select {
				case got := <-legacy:
					t.Fatalf("legacy returned while persistence held: %+v", got)
				default:
				}
				unblock()
				if got := stopAwait(t, legacy); got.err != nil || got.row.Process != "exited" {
					t.Fatalf("legacy completion: %+v", got)
				}
				for range 8 {
					got := stopAwait(t, retries)
					if got.err != nil || got.row.Process != "exited" {
						t.Fatalf("join: %+v", got)
					}
				}
				if err := svc.Session.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				if phase == "catalog" {
					if err := stopAwait(t, barrierDone); err != nil {
						t.Fatal(err)
					}
				}
				events, err := svc.Store.EventsSince(0, 100)
				if err != nil {
					t.Fatal(err)
				}
				counts := map[string]int{}
				for _, e := range events {
					counts[e.Kind]++
				}
				if counts["session.exited"] != 1 || counts["session.stopped"] != 1 {
					t.Fatalf("duplicate/missing effects: %+v", events)
				}
				stored, err := svc.Store.GetSession(live.ID)
				if err != nil || stored.Process != "exited" || stored.EndedAt == nil {
					t.Fatalf("durability: %+v %v", stored, err)
				}
			})
		}
	}
}

func TestStopSessionContextCanceledFresh(t *testing.T) {
	svc, _ := persistenceSvc(t)
	t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
	live, err := svc.Session.Start(domain.Session{ID: "untouched", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSession(live); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("already canceled")
	cancel(cause)
	got, err := svc.StopSessionContext(ctx, live.ID)
	var pending *session.FinalizationPendingError
	if !errors.Is(err, cause) || errors.As(err, &pending) || got.Process != "running" {
		t.Fatalf("fresh canceled: %+v %v", got, err)
	}
	if _, err := svc.Session.Take(live.ID, "tui", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Session.Input(live.ID, "owner", []byte("still alive\n")); err != nil {
		t.Fatal(err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("canceled request mutated catalog: %+v %v", events, err)
	}
	if _, err := svc.StopSession(live.ID); err != nil {
		t.Fatal(err)
	}
}

// Done observes the foreground select without changing cancellation behavior.
type stopJoinContext struct {
	context.Context
	once   sync.Once
	joined chan struct{}
}

func (c *stopJoinContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

var stopSQLBarrierID atomic.Uint64

func TestStopSessionContextStoppedTransaction(t *testing.T) {
	for _, mode := range []string{"cancel", "cause", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			name := fmt.Sprintf("stop_barrier_%d", stopSQLBarrierID.Add(1))
			if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
				close(entered)
				<-release
				return int64(1), nil
			}); err != nil {
				t.Fatal(err)
			}
			svc, db := persistenceSvc(t)
			t.Cleanup(func() { unblock(); svc.Session.Shutdown(context.Background()) })
			sqlExec(t, db, `CREATE TRIGGER hold_stop BEFORE INSERT ON events WHEN NEW.kind='session.stopped' BEGIN SELECT `+name+`(); END`)
			live, err := svc.Session.Start(domain.Session{ID: "write", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutSession(live); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 2*time.Second)
				defer deadlineCancel()
			}
			done := make(chan stopOutcome, 1)
			go func() { row, err := svc.StopSessionContext(ctx, live.ID); done <- stopOutcome{row, err} }()
			stopAwait(t, entered)
			if _, ok := svc.Session.Snapshot(live.ID); ok {
				t.Fatal("exit callback not fully completed before stopped transaction")
			}
			events, err := db.Query(`SELECT kind FROM events ORDER BY seq`)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []string
			for events.Next() {
				var kind string
				if err := events.Scan(&kind); err != nil {
					t.Fatal(err)
				}
				kinds = append(kinds, kind)
			}
			if err := events.Close(); err != nil {
				t.Fatal(err)
			}
			if len(kinds) != 1 || kinds[0] != "session.exited" {
				t.Fatalf("uncommitted stopped event visible: %v", kinds)
			}
			cause := error(context.Canceled)
			if mode == "cause" {
				cause = errors.New("stop writer caller left")
			}
			if mode == "deadline" {
				cause = context.DeadlineExceeded
				<-ctx.Done()
			} else {
				cancel(cause)
			}
			got := stopAwait(t, done)
			var pending *session.FinalizationPendingError
			if !errors.Is(got.err, cause) || !errors.As(got.err, &pending) || got.row.Process != "exited" {
				t.Fatalf("held transaction: %+v", got)
			}
			if err := svc.Session.Shutdown(ctx); !errors.Is(err, cause) {
				t.Fatalf("Store could close with transaction held: %v", err)
			}
			unblock()
			if err := svc.Session.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			persisted, err := svc.Store.EventsSince(0, 100)
			if err != nil || len(persisted) != 2 || persisted[1].Kind != "session.stopped" {
				t.Fatalf("actual commit: %+v %v", persisted, err)
			}
		})
	}
}

func TestStopSessionContextRejectedPersistenceRetry(t *testing.T) {
	for _, kind := range []string{"session.exited", "session.stopped"} {
		t.Run(kind, func(t *testing.T) {
			svc, db := persistenceSvc(t)
			t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
			sqlExec(t, db, `CREATE TRIGGER reject_stop BEFORE INSERT ON events WHEN NEW.kind='`+kind+`' BEGIN SELECT RAISE(ABORT,'required stop write rejected'); END`)
			live, err := svc.Session.Start(domain.Session{ID: "rejected", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutSession(live); err != nil {
				t.Fatal(err)
			}
			got, first := svc.StopSession(live.ID)
			if first == nil || !strings.Contains(first.Error(), "required stop write rejected") || got.Process != "exited" {
				t.Fatalf("actual rejected write: %+v %v", got, first)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			for _, retryCtx := range []context.Context{ctx, context.Background()} {
				retry, err := svc.StopSessionContext(retryCtx, live.ID)
				if !errors.Is(err, first) || retry.Process != "exited" {
					t.Fatalf("retry lost known failure: %+v %v", retry, err)
				}
			}
			drainErr := svc.Session.Shutdown(context.Background())
			if kind == "session.exited" {
				if drainErr == nil || !strings.Contains(drainErr.Error(), "required stop write rejected") {
					t.Fatalf("drain lost callback failure: %v", drainErr)
				}
			} else if drainErr != nil {
				t.Fatalf("completed reported stop write poisoned later drain: %v", drainErr)
			}
			stored, err := svc.Store.GetSession(live.ID)
			want := "exited"
			if kind == "session.exited" {
				want = "running"
			}
			if err != nil || stored.Process != want {
				t.Fatalf("transaction rollback: %+v %v", stored, err)
			}
			events, err := svc.Store.EventsSince(0, 100)
			wantEvents := 1
			if kind == "session.exited" {
				wantEvents = 0
			}
			if err != nil || len(events) != wantEvents {
				t.Fatalf("retries invented events: %+v %v", events, err)
			}
		})
	}
}

func TestStopSessionContextRejectedPersistenceDuringDrain(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	name := fmt.Sprintf("stop_barrier_%d", stopSQLBarrierID.Add(1))
	if err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	svc, db := persistenceSvc(t)
	t.Cleanup(func() { unblock(); svc.Session.Shutdown(context.Background()) })
	sqlExec(t, db, `CREATE TRIGGER reject_stop BEFORE INSERT ON events WHEN NEW.kind='session.stopped' BEGIN SELECT `+name+`(); SELECT RAISE(ABORT,'active stop write rejected'); END`)
	live, err := svc.Session.Start(domain.Session{ID: "drain-rejected", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSession(live); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan stopOutcome, 1)
	go func() { row, err := svc.StopSessionContext(ctx, live.ID); done <- stopOutcome{row, err} }()
	stopAwait(t, entered)
	if _, ok := svc.Session.Snapshot(live.ID); ok {
		t.Fatal("exit callback not fully completed before stopped transaction")
	}
	cause := errors.New("caller left before rejected write")
	cancel(cause)
	got := stopAwait(t, done)
	var pending *session.FinalizationPendingError
	if !errors.Is(got.err, cause) || !errors.As(got.err, &pending) || got.row.Process != "exited" {
		t.Fatalf("pending rejected write: %+v %v", got.row, got.err)
	}
	joined := make(chan struct{})
	joinCtx := &stopJoinContext{Context: context.Background(), joined: joined}
	go func() { row, err := svc.StopSessionContext(joinCtx, live.ID); done <- stopOutcome{row, err} }()
	stopAwait(t, joined)
	if err := svc.Session.Shutdown(ctx); !errors.Is(err, cause) {
		t.Fatalf("drain abandoned held transaction: %v", err)
	}
	unblock()
	got = stopAwait(t, done)
	var persistenceErr *session.StopPersistenceError
	if !errors.As(got.err, &persistenceErr) || !strings.Contains(got.err.Error(), "active stop write rejected") || errors.As(got.err, &pending) || got.row.Process != "exited" {
		t.Fatalf("joined rejection: %+v %v", got.row, got.err)
	}
	if err := svc.Session.Shutdown(context.Background()); !errors.Is(err, got.err) {
		t.Fatalf("active drain lost required persistence failure: %v", err)
	}
	if retry, err := svc.StopSessionContext(ctx, live.ID); !errors.Is(err, got.err) || errors.As(err, &pending) || retry.Process != "exited" {
		t.Fatalf("completed canceled retry lost failure: %+v %v", retry, err)
	}
	stored, err := svc.Store.GetSession(live.ID)
	if err != nil || stored.Process != "exited" || stored.EndedAt == nil {
		t.Fatalf("durable exit: %+v %v", stored, err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 1 || events[0].Kind != "session.exited" {
		t.Fatalf("rejected stop transaction did not roll back: %+v %v", events, err)
	}
}

func TestStopSessionContextPriorExitFailure(t *testing.T) {
	svc, db := persistenceSvc(t)
	t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
	sqlExec(t, db, `CREATE TRIGGER reject_exit BEFORE INSERT ON events WHEN NEW.kind='session.exited' BEGIN SELECT RAISE(ABORT,'prior exit rejected'); END`)
	pending, err := svc.Session.StartPending(domain.Session{ID: "prior-failure", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Persist(svc.Store.PutSession); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session.Stop(pending.Snapshot().ID); err == nil || !strings.Contains(err.Error(), "prior exit rejected") {
		t.Fatalf("direct Session stop: %v", err)
	}
	if err := svc.Session.Shutdown(context.Background()); err == nil || !strings.Contains(err.Error(), "prior exit rejected") {
		t.Fatalf("exit did not retain failure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := svc.StopSessionContext(ctx, pending.Snapshot().ID)
	if err == nil || !strings.Contains(err.Error(), "prior exit rejected") || got.Process != "exited" {
		t.Fatalf("canceled first Service stop hid prior exit failure: %+v %v", got, err)
	}
	stored, err := svc.Store.GetSession(got.ID)
	if err != nil || stored.Process != "running" {
		t.Fatalf("rejected exit mutated row: %+v %v", stored, err)
	}
}

func TestStopSessionContextPriorUnownedPersistenceFailure(t *testing.T) {
	svc, db := persistenceSvc(t)
	t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
	if err := svc.Store.PutSession(domain.Session{ID: "unowned-failure", Process: "running", PID: -1}); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, `CREATE TRIGGER reject_recovery BEFORE INSERT ON events WHEN NEW.kind='session.reconciled' BEGIN SELECT RAISE(ABORT,'recovery stop rejected'); END`)
	_, first := svc.StopSession("unowned-failure")
	if first == nil || !strings.Contains(first.Error(), "recovery stop rejected") {
		t.Fatalf("real persistence failure: %v", first)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.StopSessionContext(ctx, "unowned-failure"); !errors.Is(err, first) {
		t.Fatalf("canceled retry hid known catalog failure: %v", err)
	}
	sqlExec(t, db, `DROP TRIGGER reject_recovery`)
	if _, err := svc.StopSession("unowned-failure"); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("uncanceled recovery retry: %v", err)
	}
	if _, err := svc.StopSessionContext(ctx, "unowned-failure"); !errors.Is(err, context.Canceled) || errors.Is(err, first) {
		t.Fatalf("resolved failure retained: %v", err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 1 || events[0].Kind != "session.reconciled" {
		t.Fatalf("recovery effects: %+v %v", events, err)
	}
}
