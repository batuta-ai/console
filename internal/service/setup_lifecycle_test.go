package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

func TestExplicitInitLifecycle(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			f, cli, out := commandFixture(t, adapter)
			done := make(chan domain.Session, 1)
			onExit := f.svc.Session.OnExit
			f.svc.Session.OnExit = func(row domain.Session) { onExit(row); done <- row }
			input := confirmedFixtureInput(f.account.ID)
			input.Config.Test = "literal test; $(do-not-run) `not-a-command`"
			brief := "literal notes; $(never-run)\nsecond line"
			caller := filepath.Join(f.workspace.Root, "WORK.md")
			if err := os.WriteFile(caller, []byte("caller history"), 0600); err != nil {
				t.Fatal(err)
			}
			live, err := f.svc.StartInit(f.workspace.ID, brief, "web", "first", input)
			if err != nil {
				t.Fatal(err)
			}
			observedCommand(t, f, live, out, []string{cli, "Run batuta-init for this project. Write .batuta/profile.md, .batuta/routing.md and WORK.md from these confirmed answers, preserving existing user content, then stop.\n\nStack: Go\nMethodology: TDD\nTest: literal test; $(do-not-run) `not-a-command`\nBuild: go build ./...\nInstall: go mod download\nExecution: sequential\nWorktree: medium+\nTemplate: templates/go.md\n\nAdditional notes:\nliteral notes; $(never-run)\nsecond line"})
			commandEvent(t, f.svc.Store, live, "workspace.init", wantCommandReport("init", "prompt", "", ""))
			got := f.svc.DecorateWorkspace(f.workspace)
			if got.Setup.State != "running" || got.Setup.Ready || got.Setup.Attempt.SessionID != live.ID || got.Setup.Attempt.AccountID != f.account.ID {
				t.Fatalf("running=%+v", got.Setup)
			}
			again, err := f.svc.StartInit(f.workspace.ID, brief, "web", "reused", input)
			if err != nil || again.ID != live.ID {
				t.Fatalf("reuse=%+v %v", again, err)
			}
			before, ok := f.svc.Session.Snapshot(live.ID)
			if !ok {
				t.Fatal("lost owner")
			}
			seq, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			changed := input
			changed.Config.Build = "different build"
			if _, err := f.svc.StartInit(f.workspace.ID, brief, "tui", "must-not-take", changed); !errors.Is(err, ErrInitConflict) {
				t.Fatalf("answers did not conflict: %v", err)
			}
			other, err := f.svc.CreateAccount("other", adapter)
			if err != nil {
				t.Fatal(err)
			}
			other, err = f.svc.ConnectAccount(other.ID)
			if err != nil {
				t.Fatal(err)
			}
			authenticateFixtureAccount(t, f.svc, other, "B")
			changed = input
			changed.AccountID = other.ID
			if _, err := f.svc.StartInit(f.workspace.ID, brief, "tui", "must-not-take", changed); !errors.Is(err, ErrInitConflict) {
				t.Fatalf("identity did not conflict: %v", err)
			}
			after, ok := f.svc.Session.Snapshot(live.ID)
			if !ok || after.OwnerClient != before.OwnerClient || after.Owner != before.Owner {
				t.Fatalf("conflict changed ownership: %+v", after)
			}
			events, err := f.svc.Store.EventsSince(seq, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Kind == "workspace.init" {
					t.Fatal("conflict emitted new init")
				}
			}
			if _, err := f.svc.StopSession(live.ID); err != nil {
				t.Fatal(err)
			}
			waitExit(t, done)
			got = f.svc.DecorateWorkspace(f.workspace)
			if got.Setup.State != "cancelled" || !got.Setup.Attempt.StopRequested || got.Setup.Attempt.State != "cancel_requested" {
				t.Fatalf("stopped=%+v", got.Setup)
			}
			next, err := f.svc.StartInit(f.workspace.ID, brief, "web", "retry", changed)
			if err != nil {
				t.Fatal(err)
			}
			if next.ID == live.ID || next.AccountID != other.ID {
				t.Fatalf("retry identity=%+v", next)
			}
			if data, err := os.ReadFile(caller); err != nil || string(data) != "caller history" {
				t.Fatalf("caller file changed: %q %v", data, err)
			}
			rows, err := f.svc.Store.ListSessions()
			if err != nil || len(rows) != 2 {
				t.Fatalf("history=%v %v", rows, err)
			}
			if _, err := f.svc.StopSession(next.ID); err != nil {
				t.Fatal(err)
			}
			waitExit(t, done)
			setupFiles(t, f.workspace.Root, completeProfile, completeRouting, completeWork)
			got = f.svc.DecorateWorkspace(f.workspace)
			if !got.Setup.Ready || got.Setup.State != "ready" || got.Setup.Attempt.SessionID != next.ID || !got.Setup.Attempt.StopRequested {
				t.Fatalf("ready hid history: %+v", got.Setup)
			}
		})
	}
}

func TestExplicitInitStopEventFailureRemainsUnknownAndRetryable(t *testing.T) {
	f, _, _ := commandFixture(t, "codex")
	done := make(chan domain.Session, 1)
	onExit := f.svc.Session.OnExit
	f.svc.Session.OnExit = func(row domain.Session) { onExit(row); done <- row }
	in := confirmedFixtureInput(f.account.ID)
	live, err := f.svc.StartInit(f.workspace.ID, "notes", "web", "test", in)
	if err != nil {
		t.Fatal(err)
	}
	readyProcess(t, f.svc, live)
	sqlExec(t, f.db, `CREATE TRIGGER reject_stop BEFORE INSERT ON events WHEN NEW.kind='session.stopped' BEGIN SELECT RAISE(ABORT,'stop event rejected'); END`)
	if _, err := f.svc.StopSession(live.ID); err == nil || !strings.Contains(err.Error(), "stop event rejected") {
		t.Fatalf("stop persistence error=%v", err)
	}
	waitExit(t, done)
	got := f.svc.DecorateWorkspace(f.workspace)
	if got.Setup.State != "partial" || got.Setup.Code != "init_exited_incomplete" || got.Setup.Attempt.StopRequested {
		t.Fatalf("fabricated cancellation: %+v", got.Setup)
	}
	sqlExec(t, f.db, `DROP TRIGGER reject_stop`)
	next, err := f.svc.StartInit(f.workspace.ID, "retry notes", "web", "test", in)
	if err != nil || next.ID == live.ID {
		t.Fatalf("retry=%+v %v", next, err)
	}
	if _, err := f.svc.StopSession(next.ID); err != nil {
		t.Fatal(err)
	}
	waitExit(t, done)
}

func TestExplicitInitCancellationAndMissingSelectionDoNotAdmit(t *testing.T) {
	svc, _ := persistenceSvc(t)
	w, err := svc.CreateWorkspace("synthetic", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var err error
		if missing {
			_, err = svc.StartInit(w.ID, "brief only", "web", "test")
		} else {
			_, err = svc.StartInitContext(ctx, w.ID, "notes", "web", "test", confirmedFixtureInput("selected"))
		}
		want := "check_cancelled"
		if missing {
			want = "authenticated_account_required"
		}
		if code, ok := ReadinessFailure(err); !ok || code != want {
			t.Fatalf("rejection=%v", err)
		}
	}
	rows, err := svc.Store.ListSessions()
	if err != nil || len(rows) != 0 {
		t.Fatalf("accepted=%v %v", rows, err)
	}
}

func TestSetupRestartPreservesAttemptWithoutAdoptingPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, session.New(t.TempDir()), nil)
	w, err := svc.CreateWorkspace("synthetic", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	process, err := session.InspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	old := domain.Session{ID: "old-attempt", WorkspaceID: w.ID, AccountID: "selected", Work: "init", Process: "running", PID: os.Getpid(), ProcessIdentity: process.Identity, Owner: "web", OwnerClient: "old-client", CreatedAt: domain.Now()}
	if err := st.PutSession(old); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc = New(st, session.New(t.TempDir()), nil)
	if err := svc.ReconcileSessions(); err != nil {
		t.Fatal(err)
	}
	got := svc.DecorateWorkspace(w)
	if got.Setup.State != "interrupted" || got.Setup.Attempt.SessionID != old.ID || got.Setup.Attempt.AccountID != old.AccountID || got.Setup.Attempt.Process != "unknown" {
		t.Fatalf("restart=%+v", got.Setup)
	}
	if _, ok := svc.Session.Snapshot(old.ID); ok {
		t.Fatal("adopted old PID")
	}
	before, err := st.GetSession(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StopSession(old.ID); !errors.Is(err, ErrSessionNotOwned) {
		t.Fatalf("stopped foreign process: %v", err)
	}
	after, err := st.GetSession(old.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("old history changed: %+v %v", after, err)
	}
	setupFiles(t, w.Root, completeProfile, completeRouting, completeWork)
	got = svc.DecorateWorkspace(w)
	if got.Setup.State != "ready" || !got.Setup.Ready || got.Setup.Attempt.State != "interrupted" {
		t.Fatalf("readiness hides interruption: %+v", got.Setup)
	}
}

func TestSetupNativeTerminationEvidence(t *testing.T) {
	for _, outcome := range []string{"exit-zero", "exit-nonzero", "stop", "stop-event-failure"} {
		t.Run(outcome, func(t *testing.T) {
			svc, db := persistenceSvc(t)
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan domain.Session, 1)
			onExit := svc.Session.OnExit
			svc.Session.OnExit = func(row domain.Session) { onExit(row); done <- row }
			argv := []string{"/bin/cat"}
			if outcome == "exit-zero" {
				argv = []string{"/bin/sh", "-c", "exit 0"}
			}
			if outcome == "exit-nonzero" {
				argv = []string{"/bin/sh", "-c", "exit 23"}
			}
			row := domain.Session{ID: "native-attempt", WorkspaceID: w.ID, AccountID: "selected-account", Work: "init", Process: "running", Argv: argv, CreatedAt: domain.Now()}
			live, err := svc.startAndPersist(row, nil, w.Root, 0, "workspace.init", map[string]any{"session": row.ID})
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "stop" || outcome == "stop-event-failure" {
				if outcome == "stop-event-failure" {
					sqlExec(t, db, `CREATE TRIGGER reject_stop BEFORE INSERT ON events WHEN NEW.kind='session.stopped' BEGIN SELECT RAISE(ABORT,'stop event rejected'); END`)
				}
				_, err := svc.StopSession(live.ID)
				if outcome == "stop" && err != nil {
					t.Fatal(err)
				}
				if outcome == "stop-event-failure" && (err == nil || !strings.Contains(err.Error(), "stop event rejected")) {
					t.Fatalf("stop failure=%v", err)
				}
			}
			waitExit(t, done)
			got := svc.DecorateWorkspace(w)
			want := outcome == "stop"
			if got.Setup.Ready || got.Setup.Attempt == nil || got.Setup.Attempt.StopRequested != want || got.Setup.Attempt.Process != "exited" {
				t.Fatalf("termination=%+v", got.Setup)
			}
			if want {
				if got.Setup.State != "cancelled" {
					t.Fatalf("stop=%+v", got.Setup)
				}
			} else if got.Setup.State != "partial" || got.Setup.Code != "init_exited_incomplete" {
				t.Fatalf("invented termination outcome: %+v", got.Setup)
			}
			setupFiles(t, w.Root, completeProfile, completeRouting, completeWork)
			got = svc.DecorateWorkspace(w)
			if !got.Setup.Ready || got.Setup.State != "ready" || got.Setup.Attempt.StopRequested != want {
				t.Fatalf("configuration/termination conflated: %+v", got.Setup)
			}
		})
	}
}
