package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const neverShipInitBranch = "batuta/windows-concurrent-init-phase-diagnostic"

// The marker is supplied only by the exact-branch CI condition or Root's
// source-verified native staging. It is not a substitute for that source proof.
func neverShipInitTraceEnabled() bool {
	return os.Getenv("NEVER_SHIP_INIT_TRACE") == "1" && os.Getenv("NEVER_SHIP_INIT_TRACE_BRANCH") == neverShipInitBranch
}

type neverShipInitReporter struct {
	requests [10]neverShipInitTrace
}

func neverShipInitStartReporter(t *testing.T, mode string) *neverShipInitReporter {
	t.Helper()
	if mode != "init" || !neverShipInitTraceEnabled() {
		return nil
	}
	records := make(chan neverShipInitRecord, 10*neverShipInitRecordLimit)
	reporter := &neverShipInitReporter{}
	for i := range reporter.requests {
		reporter.requests[i].id = i + 1
		reporter.requests[i].records = records
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		emit := func(r neverShipInitRecord) {
			data, err := json.Marshal(r)
			if err != nil || len(data) > 512 {
				t.Log("NEVER_SHIP_INIT_TRACE invalid bounded record")
				return
			}
			t.Logf("NEVER_SHIP_INIT_TRACE %s", data)
		}
		for {
			select {
			case r := <-records:
				emit(r)
			case <-stop:
				for len(records) != 0 {
					emit(<-records)
				}
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
		for i := range reporter.requests {
			t.Logf("NEVER_SHIP_INIT_TRACE_END request_id=%d truncated=%t", i+1, reporter.requests[i].lost.Load())
		}
	})
	return reporter
}

func (r *neverShipInitReporter) context(id int) context.Context {
	trace := &r.requests[id-1]
	trace.start = time.Now()
	return context.WithValue(context.Background(), neverShipInitTraceKey{}, trace)
}

func TestNeverShipInitTraceGate(t *testing.T) {
	for _, tc := range []struct {
		enabled, branch string
		want            bool
	}{
		{"", "", false}, {"", neverShipInitBranch, false}, {"0", neverShipInitBranch, false},
		{"1", "", false}, {"1", "main", false}, {"1", neverShipInitBranch + "-other", false},
		{"1", neverShipInitBranch, true},
	} {
		t.Setenv("NEVER_SHIP_INIT_TRACE", tc.enabled)
		t.Setenv("NEVER_SHIP_INIT_TRACE_BRANCH", tc.branch)
		if neverShipInitTraceEnabled() != tc.want {
			t.Fatalf("incorrect exact-branch gate: %+v", tc)
		}
		if !tc.want && neverShipInitStartReporter(t, "init") != nil {
			t.Fatal("default-off must not allocate a reporter")
		}
		if neverShipInitStartReporter(t, "login") != nil {
			t.Fatal("login must remain unobserved")
		}
	}
}

func neverShipInitTestContext(id int, capacity int) (context.Context, *neverShipInitTrace, chan neverShipInitRecord) {
	records := make(chan neverShipInitRecord, capacity)
	trace := &neverShipInitTrace{id: id, start: time.Now(), records: records}
	return context.WithValue(context.Background(), neverShipInitTraceKey{}, trace), trace, records
}

// This tests the real public service rejection, without a listener or a probe.
func TestNeverShipInitTraceServiceResult(t *testing.T) {
	svc, _ := persistenceSvc(t)
	want, wantErr := svc.StartInit("missing", "", "web", "test")
	ctx, _, records := neverShipInitTestContext(1, neverShipInitRecordLimit)
	got, gotErr := svc.StartInitContext(ctx, "missing", "", "web", "test")
	if !reflect.DeepEqual(got, want) || !errors.Is(wantErr, sql.ErrNoRows) || !errors.Is(gotErr, sql.ErrNoRows) {
		t.Fatalf("public result changed: %v / %v", wantErr, gotErr)
	}
	if len(records) != 2 {
		t.Fatalf("want validation entry and authoritative return, got %d records", len(records))
	}
	entry, returned := <-records, <-records
	if entry.Phase != "validation_enter" || returned.Phase != "returned" || returned.ErrorClass != "catalog" {
		t.Fatalf("unexpected phases: %+v %+v", entry, returned)
	}
	if entry.RemainingNS == nil || *entry.RemainingNS > int64(5*time.Second) || returned.ContextErr != "none" || returned.Cause != "none" {
		t.Fatal("original deadline or pre-cancel return observation missing")
	}
	if ctx.Err() != nil {
		t.Fatal("observer changed caller lifetime")
	}
}

func TestNeverShipInitTraceServiceCancelled(t *testing.T) {
	svc, _ := persistenceSvc(t)
	ws, err := svc.CreateWorkspace("trace", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, expired := range []bool{false, true} {
		ctx, _, records := neverShipInitTestContext(2, neverShipInitRecordLimit)
		var cancel context.CancelFunc
		wantCause := "cancelled"
		if expired {
			ctx, cancel = context.WithDeadline(ctx, time.Unix(1, 0))
			wantCause = "deadline"
		} else {
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		before := context.Cause(ctx)
		got, gotErr := svc.StartInitContext(ctx, ws.ID, "brief", "web", "test", confirmedFixtureInput("missing"))
		cancel()
		if code, ok := ReadinessFailure(gotErr); !ok || code != "check_cancelled" || got.ID != "" {
			t.Fatalf("original cancellation rejection changed: %v", gotErr)
		}
		if context.Cause(ctx) != before || len(records) != 3 {
			t.Fatal("caller cause or validation-only path changed")
		}
		for range 2 {
			<-records
		}
		r := <-records
		if r.Phase != "returned" || r.ContextErr != wantCause || r.Cause != wantCause || r.ErrorClass != "check_cancelled" {
			t.Fatalf("incorrect authoritative return record: %+v", r)
		}
	}
}

func TestNeverShipInitTraceReporterPublicRejection(t *testing.T) {
	t.Setenv("NEVER_SHIP_INIT_TRACE", "1")
	t.Setenv("NEVER_SHIP_INIT_TRACE_BRANCH", neverShipInitBranch)
	svc, _ := persistenceSvc(t)
	reporter := neverShipInitStartReporter(t, "init")
	ctx := reporter.context(1)
	if _, ok := ctx.Deadline(); ok || ctx.Done() != nil {
		t.Fatal("observation context must retain Background lifetime")
	}
	if _, err := svc.StartInitContext(ctx, "missing", "", "web", "test"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reporter changed public rejection: %v", err)
	}
}

func TestNeverShipInitTraceBounds(t *testing.T) {
	ctx, trace, records := neverShipInitTestContext(10, neverShipInitRecordLimit)
	for range neverShipInitRecordLimit + 2 {
		neverShipInitObserve(ctx, "probe_exit", errors.New("secret-body-header-token-path"), -1)
	}
	if len(records) != neverShipInitRecordLimit || !trace.lost.Load() {
		t.Fatalf("bounded records: %d, truncated: %v", len(records), trace.lost.Load())
	}
	var previous int64
	for i := range neverShipInitRecordLimit {
		r := <-records
		data, err := json.Marshal(r)
		if err != nil || len(data) > 512 || strings.Contains(string(data), "secret") {
			t.Fatal("unbounded or unsafe record")
		}
		if r.RequestID != 10 || r.Sequence != i+1 || r.ElapsedNS < previous || r.RemainingNS != nil || r.ExitCode != nil || r.ErrorClass != "other" {
			t.Fatalf("invalid record: %+v", r)
		}
		previous = r.ElapsedNS
	}
}

func TestNeverShipInitTraceNoObserverOrInvalidID(t *testing.T) {
	neverShipInitObserve(context.Background(), "validation_enter", nil, -1)
	for _, id := range []int{0, 11} {
		ctx, _, records := neverShipInitTestContext(id, 1)
		neverShipInitObserve(ctx, "validation_enter", nil, -1)
		if len(records) != 0 {
			t.Fatal("invalid request ID recorded")
		}
	}
}

type neverShipInitHostileError struct{}

func (neverShipInitHostileError) Error() string { panic("must not format error content") }
func (neverShipInitHostileError) Unwrap() error { panic("must not invoke error callbacks") }

func TestNeverShipInitTraceNonsemantic(t *testing.T) {
	ctx, trace, records := neverShipInitTestContext(1, 4)
	parent, cancel := context.WithCancelCause(ctx)
	cause := neverShipInitHostileError{}
	cancel(cause)
	neverShipInitObserve(parent, "returned", cause, -1)
	if len(records) != 1 {
		t.Fatal("missing cancellation observation")
	}
	r := <-records
	if r.ContextErr != "cancelled" || r.Cause != "other" || r.ErrorClass != "other" || context.Cause(parent) != cause {
		t.Fatalf("cause changed or callback invoked: %+v", r)
	}
	trace.busy.Store(true)
	neverShipInitObserve(parent, "returned", nil, -1)
	trace.busy.Store(false)
	if len(records) != 0 || !trace.lost.Load() {
		t.Fatal("reentrant observation must be dropped")
	}
	neverShipInitObserve(ctx, "secret-phase", nil, 0)
	if len(records) != 0 {
		t.Fatal("unqualified phase recorded")
	}
	neverShipInitObserve(ctx, "probe_exit", nil, 0)
	r = <-records
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatal("known zero exit lost")
	}
	ctx, trace, _ = neverShipInitTestContext(2, 0)
	neverShipInitObserve(ctx, "validation_enter", nil, -1)
	if !trace.lost.Load() || ctx.Err() != nil {
		t.Fatal("full reporter must drop without cancellation or blocking")
	}
}
