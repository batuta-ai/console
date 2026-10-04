package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/store"
)

const completeProfile = "# Profile — synthetic\nStack: Go\nMethodology: TDD\nTest: never-execute-test\nBuild: never-execute-build\nInstall: never-execute-install\nExecution: sequential\nWorktree: medium+\nTemplate: templates/go.md\n"
const completeRouting = "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | synthetic |\n| medium | * | codex | default |\n| high | * | claude | synthetic |\n| critical | * | self | session |\n"
const completeWork = "# Work — synthetic\n\n## In progress\n\n## Done\n"

func setupFiles(t *testing.T, root, profile, routing, work string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".batuta"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{".batuta/profile.md": profile, ".batuta/routing.md": routing, "WORK.md": work} {
		if value == "<missing>" {
			continue
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func setupService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil, nil)
}

func TestSetupReadinessRequiresCompleteFiles(t *testing.T) {
	for _, tc := range []struct {
		name, profile, routing, work string
		ready                        bool
	}{
		{"complete", completeProfile, completeRouting, completeWork, true},
		{"empty-profile", "", completeRouting, completeWork, false},
		{"partial-profile", "Stack: Go\n", completeRouting, completeWork, false},
		{"malformed-profile", "not a profile", completeRouting, completeWork, false},
		{"invalid-execution", strings.ReplaceAll(completeProfile, "sequential", "maybe"), completeRouting, completeWork, false},
		{"invalid-worktree", strings.ReplaceAll(completeProfile, "medium+", "maybe"), completeRouting, completeWork, false},
		{"first-empty-wins", "Stack: \n" + completeProfile, completeRouting, completeWork, false},
		{"missing-routing", completeProfile, "<missing>", completeWork, false},
		{"invalid-routing", completeProfile, "not a routing table", completeWork, false},
		{"partial-routing", completeProfile, "| Lane | Executor | Model |\n|---|---|---|\n| low | codex | synthetic |\n", completeWork, false},
		{"missing-work", completeProfile, completeRouting, "<missing>", false},
		{"invalid-work", completeProfile, completeRouting, "just text", false},
		{"partial-work", completeProfile, completeRouting, "# Work — synthetic\n## In progress\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := setupService(t)
			root := t.TempDir()
			setupFiles(t, root, tc.profile, tc.routing, tc.work)
			got := svc.DecorateWorkspace(domain.Workspace{Root: root})
			if got.Profile != tc.ready {
				t.Fatalf("configuration readiness=%v want %v", got.Profile, tc.ready)
			}
		})
	}
}

func TestSetupReadinessRegularBoundedFiles(t *testing.T) {
	for _, kind := range []string{"directory", "oversize", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			svc := setupService(t)
			root := t.TempDir()
			setupFiles(t, root, completeProfile, completeRouting, completeWork)
			path := filepath.Join(root, ".batuta/profile.md")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(path, []byte(completeProfile+strings.Repeat("x", 1<<20)), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "profile")
				if err := os.WriteFile(target, []byte(completeProfile), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if svc.DecorateWorkspace(domain.Workspace{Root: root}).Profile {
				t.Fatal("nonregular/oversize configuration is ready")
			}
		})
	}
}

func TestSetupInitInvalidRootBeforeAccountProbe(t *testing.T) {
	for _, kind := range []string{"missing", "file"} {
		t.Run(kind, func(t *testing.T) {
			svc := setupService(t)
			root := filepath.Join(t.TempDir(), "root")
			if kind == "file" {
				if err := os.WriteFile(root, []byte("caller"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			w := domain.Workspace{ID: "workspace", Root: root, CreatedAt: domain.Now()}
			if err := svc.Store.PutWorkspace(w); err != nil {
				t.Fatal(err)
			}
			_, err := svc.StartInit(w.ID, "answers", "web", "client", confirmedFixtureInput("selected"))
			if err == nil || !strings.Contains(err.Error(), "invalid_workspace") {
				t.Fatalf("root must reject before identity/probe: %v", err)
			}
			rows, e := svc.Store.ListSessions()
			if e != nil || len(rows) != 0 {
				t.Fatalf("rows=%v err=%v", rows, e)
			}
			seq, e := svc.Store.LastEventSeq()
			if e != nil || seq != 0 {
				t.Fatalf("seq=%v err=%v", seq, e)
			}
		})
	}
}

func TestSetupCatalogFailureDoesNotPersistDraft(t *testing.T) {
	svc, db := persistenceSvc(t)
	q := domain.Squad{Name: "candidate", Members: []domain.Member{{ID: "member", Role: "maestro", CLI: "codex", AccountID: "missing", Maestro: true}}}
	if _, err := db.Exec("ALTER TABLE accounts RENAME TO accounts_unavailable"); err != nil {
		t.Fatal(err)
	}
	v := svc.ValidateDraft(q)
	if v.Ready || !strings.Contains(v.Title, "catalog_failure") {
		t.Errorf("storage hidden as missing/auth: %+v", v)
	}
	if _, err := svc.SaveSquad(q); err == nil {
		t.Error("save masked structural lookup error")
	}
	rows, err := svc.Store.ListSquads()
	if err != nil || len(rows) != 0 {
		t.Fatalf("Draft persisted on catalog failure: %v %v", rows, err)
	}
}

func TestSetupDurableAttemptStates(t *testing.T) {
	for _, tc := range []struct {
		process           string
		stopped, complete bool
		state, attempt    string
	}{
		{"exited", false, false, "partial", "exited_incomplete"},
		{"exited", true, false, "cancelled", "cancel_requested"},
		{"unknown", false, false, "interrupted", "interrupted"},
		{"unknown", true, false, "interrupted", "interrupted"},
		{"exited", true, true, "ready", "cancel_requested"},
		{"unknown", false, true, "ready", "interrupted"},
		{"exited", false, true, "ready", "exited"},
	} {
		t.Run(tc.process+tc.state+tc.attempt, func(t *testing.T) {
			svc, _ := persistenceSvc(t)
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if tc.complete {
				setupFiles(t, w.Root, completeProfile, completeRouting, completeWork)
			}
			row := domain.Session{ID: "exact-attempt", WorkspaceID: w.ID, AccountID: "selected-account", Work: "init", Process: tc.process, CreatedAt: domain.Now(), RecoveryReason: "process_missing"}
			if err := svc.Store.PutSession(row); err != nil {
				t.Fatal(err)
			}
			if tc.stopped {
				if _, err := svc.Store.AppendEvent("session.stopped", map[string]any{"id": row.ID}); err != nil {
					t.Fatal(err)
				}
			}
			got := svc.DecorateWorkspace(w)
			if got.Setup.State != tc.state || got.Setup.Ready != tc.complete || got.Setup.Attempt == nil {
				t.Fatalf("setup=%+v", got.Setup)
			}
			a := got.Setup.Attempt
			if a.SessionID != row.ID || a.AccountID != row.AccountID || a.State != tc.attempt || a.StopRequested != tc.stopped || a.Process != tc.process {
				t.Fatalf("attempt=%+v", a)
			}
		})
	}
}

func confirmedFixtureInput(account string) InitInput {
	return InitInput{AccountID: account, Config: domain.SetupConfig{Stack: "Go", Methodology: "TDD", Test: "go test ./...", Build: "go build ./...", Install: "go mod download", Execution: "sequential", Worktree: "medium+", Template: "templates/go.md"}}
}

func TestSetupSeedCatalogFailurePropagates(t *testing.T) {
	svc, db := persistenceSvc(t)
	if _, err := db.Exec("ALTER TABLE squads RENAME TO squads_unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBatutaCatalog(); err == nil || strings.Contains(err.Error(), "no rows") {
		t.Fatalf("seed masked storage: %v", err)
	}
	seq, err := svc.Store.LastEventSeq()
	if err != nil || seq != 0 {
		t.Fatalf("seed event=%d %v", seq, err)
	}
}

func TestSetupStartPersistenceClassifiedAsStorage(t *testing.T) {
	svc, db := persistenceSvc(t)
	rejectEvents(t, db)
	done := make(chan domain.Session, 1)
	onExit := svc.Session.OnExit
	svc.Session.OnExit = func(row domain.Session) { onExit(row); done <- row }
	row := domain.Session{ID: "init-storage", Work: "init", Argv: []string{"/bin/cat"}, CreatedAt: domain.Now()}
	live, err := svc.startAndPersist(row, nil, t.TempDir(), 0, "workspace.init", map[string]any{"session": row.ID})
	if live.PID == 0 {
		t.Fatalf("native process unavailable: %v", err)
	}
	waitExit(t, done)
	if !CatalogReferenceFailure(err) || !strings.Contains(err.Error(), "event rejected") {
		t.Fatalf("catalog error not classified: %v", err)
	}
}

func TestSetupUnownedRunningRowIsNotLive(t *testing.T) {
	svc := setupService(t)
	w, err := svc.CreateWorkspace("synthetic", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSession(domain.Session{ID: "stale-running", WorkspaceID: w.ID, Work: "init", Process: "running", AccountID: "selected", PID: os.Getpid(), CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	got := svc.DecorateWorkspace(w)
	if got.Setup.State != "interrupted" || got.Setup.Attempt == nil || got.Setup.Attempt.Process != "unknown" || got.Setup.Attempt.RecoveryReason == "" {
		t.Fatalf("claimed unowned process live: %+v", got.Setup.Attempt)
	}
}
