package service

import (
	"bytes"
	"log"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestExitFailureReleasesRuntimeAdmission(t *testing.T) {
	for _, mode := range []string{"natural", "stop"} {
		t.Run(mode, func(t *testing.T) {
			f := admissionSetup(t, 1)
			// The shell exits only on an explicit PTY message.
			cli := filepath.Join(filepath.Dir(f.launches), testfixture.ExecutableName("codex"))
			if err := testfixture.RewriteScript(t, cli, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' 'codex-cli 0.160.0'; exit 0; fi\nread line\n"), 0755); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			done := make(chan domain.Session, 4)
			onExit := f.svc.Session.OnExit
			f.svc.Session.OnExit = func(s domain.Session) error { err := onExit(s); done <- s; return err }
			live, err := f.start()
			if err != nil {
				t.Fatal(err)
			}
			sqlExec(t, f.db, `CREATE TRIGGER reject_exit BEFORE INSERT ON events WHEN NEW.kind IN ('session.exited','session.stopped') BEGIN SELECT RAISE(ABORT,'exit rejected'); END`)
			if mode == "natural" {
				if err := f.svc.Session.Input(live.ID, "test", []byte("quit\n")); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.svc.StopSession(live.ID); err == nil || !strings.Contains(err.Error(), "exit rejected") {
					t.Fatalf("stop persistence error: %v", err)
				}
			}
			exited := waitExit(t, done)
			if exited.Process != "exited" || exited.EndedAt == nil {
				t.Fatal(exited)
			}
			if !strings.Contains(output.String(), "exit rejected") || !strings.Contains(output.String(), live.ID) {
				t.Fatalf("missing async error log: %s", output.String())
			}
			stored, err := f.svc.Store.GetSession(live.ID)
			if err != nil || stored.Process != "running" {
				t.Fatalf("exit transaction did not roll back: %+v %v", stored, err)
			}
			next, err := f.start()
			if err != nil {
				t.Fatalf("runtime admission leaked behind stale row: %v", err)
			}
			sqlExec(t, f.db, "DROP TRIGGER reject_exit")
			if stopped, err := f.svc.StopSession(next.ID); err != nil {
				stacks := make([]byte, 256<<10)
				n := runtime.Stack(stacks, true)
				t.Logf("second Stop failure mode=%q first_launch_id=%q first_pid=%d first_identity=%q second_launch_id=%q second_pid=%d second_identity=%q error=%v", mode, live.ID, live.PID, live.ProcessIdentity, next.ID, next.PID, next.ProcessIdentity, err)
				t.Logf("returned Stop snapshot id=%q pid=%d identity=%q process=%q ended_at=%v", stopped.ID, stopped.PID, stopped.ProcessIdentity, stopped.Process, stopped.EndedAt)
				t.Logf("second Stop all-goroutine stacks captured=%d limit=%d truncated=%t\n%s", n, len(stacks), n == len(stacks), stacks[:n])
				t.Fatal(err)
			}
			waitExit(t, done)
		})
	}
}

func TestTerminatedPendingInitialPersistence(t *testing.T) {
	for _, kind := range []string{"session.started", "account.login", "workspace.init"} {
		for _, exitFailure := range []bool{false, true} {
			t.Run(kind+map[bool]string{true: "/failed-exit", false: "/persisted-exit"}[exitFailure], func(t *testing.T) {
				f := admissionSetup(t, 1)
				done := make(chan domain.Session, 1)
				onExit := f.svc.Session.OnExit
				f.svc.Session.OnExit = func(s domain.Session) error { err := onExit(s); done <- s; return err }
				if exitFailure {
					sqlExec(t, f.db, `CREATE TRIGGER reject_exit BEFORE INSERT ON events WHEN NEW.kind='session.exited' BEGIN SELECT RAISE(ABORT,'exit rejected'); END`)
				}
				pending, err := f.svc.Session.StartPending(domain.Session{ID: "late", AccountID: f.account.ID, Argv: testfixture.Argv(t, []string{"/usr/bin/true"})}, nil, "", 1)
				if err != nil {
					t.Fatal(err)
				}
				terminal := waitExit(t, done)
				payload := map[string]any{"id": f.account.ID, "session": "late"}
				if kind == "session.started" {
					payload = map[string]any{"id": "late", "adapter": "codex", "batuta": false}
				}
				if kind == "workspace.init" {
					payload["id"] = f.workspace.ID
				}
				got, err := pending.Persist(func(latest domain.Session) error { return f.svc.Store.PutSessionWithEvent(latest, kind, payload) })
				if err != nil {
					t.Fatal(err)
				}
				stored, err := f.svc.Store.GetSession("late")
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range []domain.Session{got, stored} {
					if s.Process != "exited" || s.EndedAt == nil || terminal.EndedAt == nil || !s.EndedAt.Equal(*terminal.EndedAt) {
						t.Fatalf("terminal snapshot lost: %+v; want %+v", s, terminal)
					}
				}
				events, err := f.svc.Store.EventsSince(0, 100)
				if err != nil {
					t.Fatal(err)
				}
				event := events[len(events)-1]
				if event.Kind != kind || !reflect.DeepEqual(event.Payload, payload) {
					t.Fatalf("changed initial event: %+v", event)
				}
			})
		}
	}
}

func TestImmediateExitStartPaths(t *testing.T) {
	for _, mode := range []string{"session", "login", "init"} {
		t.Run(mode, func(t *testing.T) {
			f := admissionSetup(t, 1)
			if err := testfixture.RewriteScript(t, filepath.Join(filepath.Dir(f.launches), testfixture.ExecutableName("codex")), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' 'codex-cli 0.160.0'; fi\nexit 0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			done := make(chan domain.Session, 1)
			onExit := f.svc.Session.OnExit
			f.svc.Session.OnExit = func(s domain.Session) error { err := onExit(s); done <- s; return err }
			for range 10 {
				var live domain.Session
				var err error
				switch mode {
				case "login":
					live, err = f.svc.StartLogin(f.account.ID, "web", "test")
				case "init":
					live, err = f.svc.StartInit(f.workspace.ID, "brief", "web", "test", confirmedFixtureInput(f.account.ID))
				default:
					live, err = f.start()
				}
				if err != nil {
					t.Fatal(err)
				}
				terminal := waitExit(t, done)
				if terminal.ID != live.ID || terminal.EndedAt == nil {
					t.Fatalf("exit: %+v; start %+v", terminal, live)
				}
				stored, err := f.svc.Store.GetSession(live.ID)
				if err != nil || stored.Process != "exited" || stored.EndedAt == nil || !stored.EndedAt.Equal(*terminal.EndedAt) {
					t.Fatalf("immediate exit revived: %+v %v", stored, err)
				}
				if live.Process == "exited" && (live.EndedAt == nil || !live.EndedAt.Equal(*terminal.EndedAt)) {
					t.Fatalf("returned incomplete exit: %+v", live)
				}
			}
		})
	}
}
