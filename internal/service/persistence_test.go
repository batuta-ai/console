package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func persistenceSvc(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); db.Close() })
	svc := New(st, session.New(t.TempDir()), nil)
	svc.Home = t.TempDir()
	return svc, db
}
func sqlExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}
func rejectEvents(t *testing.T, db *sql.DB) {
	sqlExec(t, db, `CREATE TRIGGER reject_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'event rejected'); END`)
}
func catalogRows(t *testing.T, db *sql.DB, table string) [][]any {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if err := rows.Scan(p...); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestCatalogEventFailureRollsBack(t *testing.T) {
	for _, name := range []string{"workspace", "seed", "squad", "mission", "account", "rename", "connect", "stop"} {
		t.Run(name, func(t *testing.T) {
			svc, db := persistenceSvc(t)
			a, err := svc.CreateAccount("original", "codex")
			if err != nil {
				t.Fatal(err)
			}
			q, err := svc.SaveSquad(domain.Squad{Name: "original"})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutSession(domain.Session{ID: "old", Process: "running", Owner: "none"}); err != nil {
				t.Fatal(err)
			}
			table := ""
			var call func() error
			switch name {
			case "workspace":
				table = "workspaces"
				call = func() error { _, err := svc.CreateWorkspace("new", t.TempDir()); return err }
			case "seed":
				table = "squads"
				call = svc.EnsureBatutaCatalog
			case "squad":
				table = "squads"
				call = func() error { q.Name = "changed"; _, err := svc.SaveSquad(q); return err }
			case "mission":
				table = "missions"
				ws, err := svc.CreateWorkspace("fixture", t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				call = func() error { _, err := svc.CreateMission(ws.ID, "new", "brief", q.ID); return err }
			case "account":
				table = "accounts"
				call = func() error { _, err := svc.CreateAccount("new", "codex"); return err }
			case "rename":
				table = "accounts"
				call = func() error { _, err := svc.RenameAccount(a.ID, "changed"); return err }
			case "connect":
				table = "accounts"
				call = func() error { _, err := svc.ConnectAccount(a.ID); return err }
			case "stop":
				table = "sessions"
				// Exercise a genuinely owned stop; catalog-only stops cannot claim exit.
				svc.Session.OnExit = nil
				owned, err := svc.Session.Start(domain.Session{ID: "old", Argv: []string{"/bin/cat"}}, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				defer svc.Session.Stop(owned.ID)
				if err := svc.Store.PutSession(owned); err != nil {
					t.Fatal(err)
				}
				call = func() error { _, err := svc.StopSession("old"); return err }
			}
			before := catalogRows(t, db, table)
			seq, err := svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			rejectEvents(t, db)
			if err := call(); err == nil || !strings.Contains(err.Error(), "event rejected") {
				t.Errorf("event failure not returned: %v", err)
			}
			if after := catalogRows(t, db, table); !reflect.DeepEqual(before, after) {
				t.Errorf("write escaped rollback: %v -> %v", before, after)
			}
			after, err := svc.Store.LastEventSeq()
			if err != nil || after != seq {
				t.Fatalf("events changed: %d %v", after, err)
			}
		})
	}
}
func TestCatalogNoOpsDoNotEmit(t *testing.T) {
	svc, db := persistenceSvc(t)
	a, err := svc.CreateAccount("same", "codex")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w, err := svc.CreateWorkspace("same", root)
	if err != nil {
		t.Fatal(err)
	}
	rejectEvents(t, db)
	if _, err := svc.RenameAccount(a.ID, "same"); err != nil {
		t.Fatal(err)
	}
	again, err := svc.CreateWorkspace("different", root)
	if err != nil || again.ID != w.ID {
		t.Fatalf("%+v %v", again, err)
	}
}
func TestOnExitReportsAtomicFailure(t *testing.T) {
	svc, db := persistenceSvc(t)
	old := domain.Session{ID: "s", Process: "running"}
	if err := svc.Store.PutSession(old); err != nil {
		t.Fatal(err)
	}
	rejectEvents(t, db)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	exited := old
	exited.Process = "exited"
	svc.Session.OnExit(exited)
	got, err := svc.Store.GetSession(old.ID)
	if err != nil || got.Process != "running" {
		t.Errorf("exit write not rolled back: %+v %v", got, err)
	}
	if !strings.Contains(output.String(), "event rejected") || !strings.Contains(output.String(), "s") {
		t.Fatalf("missing exit error log: %q", output.String())
	}
}
func waitExit(t *testing.T, done <-chan domain.Session) domain.Session {
	t.Helper()
	select {
	case s := <-done:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
		return domain.Session{}
	}
}
func TestSpawnPersistenceFailureStopsProcess(t *testing.T) {
	for _, mode := range []string{"session", "login", "init"} {
		t.Run(mode, func(t *testing.T) {
			svc, db := persistenceSvc(t)
			root := t.TempDir()
			t.Setenv("BATUTA_SKILLS", installedSkillFixture(t, t.TempDir(), "batuta", "batuta-init", "batuta-plan"))
			cli := t.TempDir()
			if err := os.WriteFile(filepath.Join(cli, "codex"), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' 'codex-cli 0.160.0'; exit 0; fi\nexec /bin/cat\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", cli)
			w, err := svc.CreateWorkspace("work", root)
			if err != nil {
				t.Fatal(err)
			}
			q, err := svc.SaveSquad(domain.Squad{Name: "q"})
			if err != nil {
				t.Fatal(err)
			}
			m, err := svc.CreateMission(w.ID, "mission", "", q.ID)
			if err != nil {
				t.Fatal(err)
			}
			a, err := svc.CreateAccount("a", "codex")
			if err != nil {
				t.Fatal(err)
			}
			a, err = svc.ConnectAccount(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			authenticateFixtureAccount(t, svc, a, "A")
			done := make(chan domain.Session, 1)
			onExit := svc.Session.OnExit
			svc.Session.OnExit = func(s domain.Session) { onExit(s); done <- s }
			rejectEvents(t, db)
			var live domain.Session
			switch mode {
			case "session":
				off := false
				live, err = svc.StartSession(StartSessionIn{MissionID: m.ID, Argv: []string{"/bin/cat"}, Inject: &off})
			case "login":
				live, err = svc.StartLogin(a.ID, "web", "c")
			case "init":
				live, err = svc.StartInit(w.ID, "brief", "web", "c", confirmedFixtureInput(a.ID))
			}
			if live.PID == 0 {
				t.Fatalf("no process started: %v", err)
			}
			if err == nil || !strings.Contains(err.Error(), "event rejected") {
				svc.Session.Stop(live.ID)
				waitExit(t, done)
				t.Fatalf("persistence failure not returned: %v", err)
			}
			exited := waitExit(t, done)
			if exited.Process != "exited" || exited.ID != live.ID {
				t.Fatal(exited)
			}
			if _, err := svc.Store.GetSession(live.ID); !store.IsNotFound(err) {
				t.Fatalf("failed start retained catalog row: %v", err)
			}
		})
	}
}
func TestReusePersistenceFailureReturned(t *testing.T) {
	for _, mode := range []string{"login", "init"} {
		t.Run(mode, func(t *testing.T) {
			f := admissionSetup(t, 0)
			svc, db, a, w := f.svc, f.db, f.account, f.workspace
			done := make(chan domain.Session, 1)
			onExit := svc.Session.OnExit
			svc.Session.OnExit = func(s domain.Session) { onExit(s); done <- s }
			argv := []string{"/bin/cat"}
			if mode == "init" {
				answers, err := confirmedInitAnswers(confirmedFixtureInput(a.ID).Config, "brief")
				if err != nil {
					t.Fatal(err)
				}
				_, command, _, _, err := svc.initCLI(context.Background(), initPrompt(answers), a.ID)
				if err != nil {
					t.Fatal(err)
				}
				argv = command.Argv
			}
			live, err := svc.Session.Start(domain.Session{ID: "reuse", AccountID: a.ID, WorkspaceID: w.ID, Work: mode, Owner: "old", Argv: argv}, nil, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { svc.Session.Stop(live.ID); waitExit(t, done) }()
			if err := svc.Store.PutSession(live); err != nil {
				t.Fatal(err)
			}
			sqlExec(t, db, `CREATE TRIGGER reject_session BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT,'session rejected'); END`)
			if mode == "login" {
				_, err = svc.StartLogin(a.ID, "web", "new")
			} else {
				_, err = svc.StartInit(w.ID, "brief", "web", "new", confirmedFixtureInput(a.ID))
			}
			if err == nil || !strings.Contains(err.Error(), "session rejected") {
				t.Fatalf("reuse error missing: %v", err)
			}
		})
	}
}

func TestAnswerEventFailureAcknowledgesExternalEffect(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			svc, db := persistenceSvc(t)
			root := t.TempDir()
			w, err := svc.CreateWorkspace("w", root)
			if err != nil {
				t.Fatal(err)
			}
			journalStore, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			questionID := "sha256:" + strings.Repeat("a", 64)
			graph, err := json.Marshal(routing.DeliveryGraph{Tasks: []routing.GraphTask{{
				TaskID: "task_1", State: routing.GraphTaskWaitingInput, Attempts: []routing.GraphTaskAttempt{{
					Execution: 1, State: routing.GraphTaskWaitingInput, ChildRunID: "run", WorktreeID: "worktree", WorktreeRoot: root,
					BaseHeadSHA: strings.Repeat("b", 40), Runtime: routing.RuntimeValue{Provider: "codex", Model: "fixture", Reasoning: "medium"},
					Question: &routing.TaskQuestion{RequestID: questionID, Prompt: "Choose", ContextDigest: "sha256:" + strings.Repeat("c", 64)},
				}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journalStore.Append("delivery", journal.Record{Kind: loop.KindOpened, Graph: graph, Detail: []byte(`{"slug":"test"}`)}); err != nil {
				t.Fatal(err)
			}
			if fail {
				rejectEvents(t, db)
			}
			id, err := svc.Answer(batuta.AnswerRequest{Workspace: w.ID, Delivery: "delivery", Task: "task_1", QuestionID: questionID, Text: "answer"})
			if id != "delivery" {
				t.Fatalf("external answer failed: %q %v", id, err)
			}
			if fail {
				if err == nil || !strings.Contains(err.Error(), "answer recorded") || !strings.Contains(err.Error(), "event rejected") {
					t.Errorf("missing partial-effect error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			records, err := journalStore.Read("delivery")
			if err != nil || len(records) != 2 || records[1].Kind != loop.KindAnswer {
				t.Fatalf("external effect: %v %v", records, err)
			}
			events, err := svc.Store.EventsSince(0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				if len(events) != 1 {
					t.Fatalf("failed event persisted: %+v", events)
				}
			} else if len(events) != 2 || events[1].Kind != "watch.answered" || !reflect.DeepEqual(events[1].Payload, map[string]any{"delivery": "delivery", "task": "task_1"}) {
				t.Fatalf("event contract: %+v", events)
			}
		})
	}
}

func TestCatalogEventContracts(t *testing.T) {
	svc, _ := persistenceSvc(t)
	var want []domain.Event
	expect := func(kind string, payload map[string]any) {
		want = append(want, domain.Event{Kind: kind, Payload: payload})
	}
	w, err := svc.CreateWorkspace("workspace", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	expect("workspace.created", map[string]any{"id": w.ID, "name": w.Name})
	q, err := svc.SaveSquad(domain.Squad{Name: "squad"})
	if err != nil {
		t.Fatal(err)
	}
	expect("squad.saved", map[string]any{"id": q.ID, "version": float64(1)})
	m, err := svc.CreateMission(w.ID, "mission", "brief", q.ID)
	if err != nil {
		t.Fatal(err)
	}
	expect("mission.created", map[string]any{"id": m.ID, "title": m.Title, "squad": q.ID, "squad_version": float64(q.Version)})
	a, err := svc.CreateAccount("account", "codex")
	if err != nil {
		t.Fatal(err)
	}
	expect("account.created", map[string]any{"id": a.ID, "adapter": a.Adapter, "isolation": string(a.Isolation)})
	a, err = svc.RenameAccount(a.ID, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	expect("account.renamed", map[string]any{"id": a.ID, "label": a.Label})
	a, err = svc.ConnectAccount(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	expect("account.connected", map[string]any{"id": a.ID, "adapter": a.Adapter, "isolate": a.VaultRef})
	owned, err := svc.Session.Start(domain.Session{ID: "s", Argv: []string{"/bin/cat"}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Session.Stop(owned.ID)
	if err := svc.Store.PutSession(owned); err != nil {
		t.Fatal(err)
	}
	expect("session.exited", map[string]any{"id": "s"})
	if _, err := svc.StopSession("s"); err != nil {
		t.Fatal(err)
	}
	expect("session.stopped", map[string]any{"id": "s"})
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != len(want) {
		t.Fatalf("event count: %+v %v", events, err)
	}
	for i, event := range events {
		if event.Kind != want[i].Kind || !reflect.DeepEqual(event.Payload, want[i].Payload) {
			t.Errorf("event %d: %+v, want %+v", i, event, want[i])
		}
	}
}
