package service

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
)

func TestCatalogOnlyStopCannotInventExit(t *testing.T) {
	svc, _ := persistenceSvc(t)
	cmd := exec.Command("/bin/cat")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	original := domain.Session{ID: "former", Process: "unknown", RecoveryReason: "identity_unverified", Owner: "none", PID: cmd.Process.Pid, Argv: []string{"/bin/cat"}, CreatedAt: domain.Now()}
	if err := svc.Store.PutSession(original); err != nil {
		t.Fatal(err)
	}
	_, err = svc.StopSession(original.ID)
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Errorf("catalog stop must report lost process ownership: %v", err)
	}
	got, err := svc.Store.GetSession(original.ID)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Errorf("catalog stop invented physical exit: %+v %v", got, err)
	}
	if _, err := input.Write([]byte("still alive\n")); err != nil {
		t.Fatal(err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 0 {
		t.Errorf("catalog stop emitted false history: %+v %v", events, err)
	}
}

func TestRecoveryObservedIncarnationAndHistory(t *testing.T) {
	svc, db := persistenceSvc(t)
	cmd := exec.Command("/bin/cat")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	observed, err := session.InspectProcess(cmd.Process.Pid)
	if err != nil || observed.Identity == "" || observed.Missing {
		t.Fatalf("native inspection: %+v %v", observed, err)
	}
	gone := exec.Command("/bin/cat")
	endInput, err := gone.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := gone.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		endInput.Close()
		if err := gone.Wait(); err != nil {
			t.Error(err)
		}
	}()
	deadIdentity, err := session.InspectProcess(gone.Process.Pid)
	if err != nil || deadIdentity.Identity == "" || deadIdentity.Missing {
		t.Fatalf("child before exit: %+v %v", deadIdentity, err)
	}
	if err := endInput.Close(); err != nil {
		t.Fatal(err)
	}
	// Leave the terminated child unreaped until every missing assertion finishes:
	// its PID cannot be reused by an unrelated process during reconciliation.
	deadline := time.Now().Add(5 * time.Second)
	for {
		dead, err := session.InspectProcess(gone.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if dead.Missing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not terminate after stdin EOF")
		}
		runtime.Gosched()
	}
	for _, tc := range []struct {
		id, identity, reason string
		pid                  int
	}{
		{"survived", observed.Identity, "process_survived", cmd.Process.Pid},
		{"mismatch", "different-incarnation", "identity_mismatch", cmd.Process.Pid},
		{"legacy", "", "identity_unverified", cmd.Process.Pid},
		{"missing", deadIdentity.Identity, "process_missing", gone.Process.Pid},
		{"missing-legacy", "", "process_missing", gone.Process.Pid},
		{"invalid", "", "identity_unverified", -1},
	} {
		original := domain.Session{ID: tc.id, MissionID: "mission", WorkspaceID: "workspace", MemberID: "member", AccountID: "account", Adapter: "codex", Argv: []string{"old", "args"}, Process: "running", Work: "interactive", Owner: "web", OwnerClient: "obsolete", PID: tc.pid, ProcessIdentity: tc.identity, Cols: 120, Rows: 40, BatutaInjected: true, SkillsVersion: "v-old", CreatedAt: domain.Now()}
		if err := svc.Store.PutSession(original); err != nil {
			t.Fatal(err)
		}
		want := original
		want.Process = "unknown"
		want.Owner = "none"
		want.OwnerClient = ""
		want.RecoveryReason = tc.reason
		if err := svc.ReconcileSessions(); err != nil {
			t.Fatal(err)
		}
		got, err := svc.Store.GetSession(tc.id)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s recovery: %+v, want %+v: %v", tc.id, got, want, err)
		}
		// Stale callers and concurrent repeated startup/GET must not emit again.
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := svc.ReconcileSession(original); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if _, err := svc.StopSession(tc.id); !errors.Is(err, ErrSessionNotOwned) {
			t.Fatalf("stop: %v", err)
		}
		if _, ok := svc.Session.Snapshot(tc.id); ok {
			t.Fatal("adopted catalog process")
		}
		if _, err := svc.Session.Take(tc.id, "web", "new"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("control adopted catalog process: %v", err)
		}
		if _, err := input.Write([]byte(tc.id + "\n")); err != nil {
			t.Fatal("former process modified: ", err)
		}
		after, err := session.InspectProcess(cmd.Process.Pid)
		if err != nil || after != observed {
			t.Fatalf("former incarnation changed: %+v %v", after, err)
		}
		t.Logf("%s: %s; former live process remains untouched", tc.id, got.RecoveryReason)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 6 {
		t.Fatalf("repeated recovery events: %+v %v", events, err)
	}
	for _, ev := range events {
		row, err := svc.Store.GetSession(ev.Payload["id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind != "session.reconciled" || ev.Payload["recovery_reason"] != row.RecoveryReason {
			t.Fatalf("event disagrees: %+v %+v", ev, row)
		}
	}
	terminal := domain.Session{ID: "terminal", Process: "exited", Owner: "tui", OwnerClient: "historical", PID: cmd.Process.Pid, ProcessIdentity: observed.Identity, RecoveryReason: "historical", Argv: []string{"history"}, CreatedAt: domain.Now()}
	ended := domain.Now()
	terminal.EndedAt = &ended
	if err := svc.Store.PutSession(terminal); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileSessions(); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Store.GetSession(terminal.ID)
	if err != nil || !reflect.DeepEqual(got, terminal) {
		t.Fatalf("terminal history changed: %+v %v", got, err)
	}
	// Reconciliation of an unrecovered running row on stop also remains truthful.
	if err := svc.Store.PutSession(domain.Session{ID: "stop-running", Process: "running", PID: cmd.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, `CREATE TRIGGER reject_recovery BEFORE INSERT ON events WHEN NEW.kind='session.reconciled' BEGIN SELECT RAISE(ABORT,'recovery rejected'); END`)
	if _, err := svc.StopSession("stop-running"); err == nil || !strings.Contains(err.Error(), "recovery rejected") {
		t.Fatal(err)
	}
	got, err = svc.Store.GetSession("stop-running")
	if err != nil || got.Process != "running" {
		t.Fatalf("rollback: %+v %v", got, err)
	}
	sqlExec(t, db, "DROP TRIGGER reject_recovery")
	if _, err := svc.StopSession("stop-running"); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatal(err)
	}
	got, err = svc.Store.GetSession("stop-running")
	if err != nil || got.Process != "unknown" || got.EndedAt != nil {
		t.Fatalf("stop invented exit: %+v %v", got, err)
	}
}

func TestRecoveryPreservesOwnedAndReleasesFreshCapacity(t *testing.T) {
	f := admissionSetup(t, 1)
	old := domain.Session{ID: "old", AccountID: f.account.ID, Process: "running", PID: os.Getpid()}
	if err := f.svc.Store.PutSession(old); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReconcileSessions(); err != nil {
		t.Fatal(err)
	}
	live, err := f.start()
	if err != nil {
		t.Fatalf("former catalog row reserved capacity: %v", err)
	}
	defer f.svc.StopSession(live.ID)
	if live.ProcessIdentity == "" {
		t.Fatal("new live process has no observed identity")
	}
	before, err := f.svc.Store.GetSession(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.ProcessIdentity != live.ProcessIdentity {
		t.Fatal("initial persistence lost incarnation")
	}
	if err := f.svc.ReconcileSessions(); err != nil {
		t.Fatal(err)
	}
	after, err := f.svc.Store.GetSession(live.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("owned session relabeled: %+v %v", after, err)
	}
}

func TestRecoveryReadFailurePropagates(t *testing.T) {
	svc, _ := persistenceSvc(t)
	svc.Store.Close()
	if err := svc.ReconcileSessions(); err == nil {
		t.Fatal("closed store recovery succeeded")
	}
}

func TestStopTerminalIsIdempotent(t *testing.T) {
	t.Run("owned first stop then repeat", func(t *testing.T) {
		svc, _ := persistenceSvc(t)
		owned, err := svc.Session.Start(domain.Session{ID: "owned", Argv: []string{"/bin/cat"}, Owner: "web", OwnerClient: "client", CreatedAt: domain.Now()}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Session.Stop(owned.ID)
		if err := svc.Store.PutSession(owned); err != nil {
			t.Fatal(err)
		}
		first, err := svc.StopSession(owned.ID)
		if err != nil || first.Process != "exited" || first.EndedAt == nil {
			t.Fatalf("owned stop: %+v %v", first, err)
		}
		events, err := svc.Store.EventsSince(0, 100)
		if err != nil || len(events) != 2 || events[0].Kind != "session.exited" || events[1].Kind != "session.stopped" {
			t.Fatalf("owned exit callback and stop events: %+v %v", events, err)
		}
		for _, event := range events {
			if !reflect.DeepEqual(event.Payload, map[string]any{"id": owned.ID}) {
				t.Fatalf("owned event payload: %+v", event)
			}
		}
		for range 3 {
			again, err := svc.StopSession(owned.ID)
			if err != nil || !reflect.DeepEqual(again, first) {
				t.Errorf("repeat stop: %+v %v; want %+v", again, err, first)
			}
		}
		row, err := svc.Store.GetSession(owned.ID)
		if err != nil || !reflect.DeepEqual(row, first) {
			t.Fatalf("terminal row changed: %+v %v", row, err)
		}
		after, err := svc.Store.EventsSince(0, 100)
		if err != nil || !reflect.DeepEqual(after, events) {
			t.Fatalf("repeat stop events changed: %+v %v", after, err)
		}
	})
	t.Run("catalog terminal with unrelated live PID", func(t *testing.T) {
		svc, db := persistenceSvc(t)
		cmd := exec.Command("/bin/cat")
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			input.Close()
			if err := cmd.Wait(); err != nil {
				t.Error(err)
			}
		}()
		before, err := session.InspectProcess(cmd.Process.Pid)
		if err != nil || before.Missing || before.Identity == "" {
			t.Fatalf("native observation: %+v %v", before, err)
		}
		ended := domain.Now()
		terminal := domain.Session{ID: "terminal", MissionID: "mission", WorkspaceID: "workspace", MemberID: "member", AccountID: "account", Adapter: "codex", Argv: []string{"old", "args"}, Process: "exited", Work: "interactive", Owner: "web", OwnerClient: "historical", PID: cmd.Process.Pid, ProcessIdentity: "former-incarnation", RecoveryReason: "historical", Cols: 120, Rows: 40, BatutaInjected: true, SkillsVersion: "v-old", CreatedAt: domain.Now(), EndedAt: &ended}
		if err := svc.Store.PutSession(terminal); err != nil {
			t.Fatal(err)
		}
		rejectEvents(t, db)
		for range 3 {
			got, err := svc.StopSession(terminal.ID)
			if err != nil || !reflect.DeepEqual(got, terminal) {
				t.Errorf("catalog terminal stop: %+v %v", got, err)
			}
		}
		got, err := svc.Store.GetSession(terminal.ID)
		if err != nil || !reflect.DeepEqual(got, terminal) {
			t.Fatalf("terminal history changed: %+v %v", got, err)
		}
		events, err := svc.Store.EventsSince(0, 100)
		if err != nil || len(events) != 0 {
			t.Fatalf("terminal no-op wrote events: %+v %v", events, err)
		}
		after, err := session.InspectProcess(cmd.Process.Pid)
		if err != nil || after != before {
			t.Fatalf("unrelated process changed: %+v %v", after, err)
		}
		if _, err := input.Write([]byte("still alive\n")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRecoveryClassifiesLegacyUnknownOnce(t *testing.T) {
	svc, db := persistenceSvc(t)
	cmd := exec.Command("/bin/cat")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	observed, err := session.InspectProcess(cmd.Process.Pid)
	if err != nil || observed.Identity == "" || observed.Missing {
		t.Fatalf("native observation: %+v %v", observed, err)
	}
	ended := domain.Now()
	original := domain.Session{ID: "legacy-unknown", MissionID: "mission", WorkspaceID: "workspace", MemberID: "member", AccountID: "account", Adapter: "codex", Argv: []string{"old", "args"}, Process: "unknown", Work: "interactive", Owner: "web", OwnerClient: "obsolete", PID: cmd.Process.Pid, Cols: 120, Rows: 40, BatutaInjected: true, SkillsVersion: "v-old", CreatedAt: domain.Now(), EndedAt: &ended}
	if err := svc.Store.PutSessionWithEvent(original, "session.started", map[string]any{"id": original.ID}); err != nil {
		t.Fatal(err)
	}
	history, err := svc.Store.EventsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	sqlExec(t, db, `CREATE TRIGGER reject_recovery BEFORE INSERT ON events WHEN NEW.kind='session.reconciled' BEGIN SELECT RAISE(ABORT,'recovery rejected'); END`)
	if err := svc.ReconcileSessions(); err == nil || !strings.Contains(err.Error(), "recovery rejected") {
		t.Errorf("legacy recovery must return transaction failure: %v", err)
	}
	got, err := svc.Store.GetSession(original.ID)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("failed recovery changed history: %+v %v", got, err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || !reflect.DeepEqual(events, history) {
		t.Fatalf("failed recovery emitted event: %+v %v", events, err)
	}
	sqlExec(t, db, "DROP TRIGGER reject_recovery")
	want := original
	want.RecoveryReason, want.Owner, want.OwnerClient = "identity_unverified", "none", ""
	for range 3 {
		if err := svc.ReconcileSessions(); err != nil {
			t.Fatal(err)
		}
		got, err := svc.ReconcileSession(original)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("legacy recovery did not classify exactly once: %+v %v", got, err)
		}
	}
	got, err = svc.StopSession(original.ID)
	if !errors.Is(err, ErrSessionNotOwned) || !reflect.DeepEqual(got, want) {
		t.Errorf("legacy stop: %+v %v", got, err)
	}
	events, err = svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 2 || !reflect.DeepEqual(events[0], history[0]) || events[1].Kind != "session.reconciled" || !reflect.DeepEqual(events[1].Payload, map[string]any{"id": original.ID, "recovery_reason": "identity_unverified"}) {
		t.Fatalf("legacy history/recovery events: %+v %v", events, err)
	}
	after, err := session.InspectProcess(cmd.Process.Pid)
	if err != nil || after != observed {
		t.Fatalf("legacy process changed: %+v %v", after, err)
	}
	if _, err := input.Write([]byte("still alive\n")); err != nil {
		t.Fatal(err)
	}
}
