package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/store"
)

func referenceSetup(t *testing.T) admissionFixture {
	t.Helper()
	f := admissionSetup(t, 0)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}

func TestReferenceRejectionBeforeSpawn(t *testing.T) {
	f := referenceSetup(t)
	control, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	readyProcess(t, f.svc, control)
	before, err := os.ReadFile(f.launches)
	if err != nil {
		t.Fatal(err)
	}
	foreignSquad, err := f.svc.SaveSquad(domain.Squad{Name: "foreign", Members: []domain.Member{{ID: "foreign"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateMission(f.workspace.ID, "foreign", "", foreignSquad.ID); err != nil {
		t.Fatal(err)
	}
	off, on := false, true
	cases := []struct {
		name                     string
		members                  []domain.Member
		member, account, adapter string
		inject                   *bool
	}{
		{name: "foreign member", member: "foreign", inject: &off},
		{name: "cross mission member", members: []domain.Member{{ID: "history"}}, member: "foreign", inject: &off},
		{name: "empty injection"},
		{name: "empty explicitly enabled injection", inject: &on},
		{name: "duplicate", members: []domain.Member{{ID: "a"}, {ID: "a"}}, inject: &off},
		{name: "unstable", members: []domain.Member{{CLI: "codex"}}, inject: &off},
		{name: "missing account", account: "missing", inject: &off},
		{name: "mismatch", account: f.account.ID, adapter: "claude", inject: &off},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := f.mission
			m.SquadSnapshot.Members = tc.members
			if err := f.svc.Store.PutMission(m); err != nil {
				t.Fatal(err)
			}
			sessions, err := f.svc.Store.ListSessions()
			if err != nil {
				t.Fatal(err)
			}
			events, err := f.svc.Store.EventsSince(0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.svc.StartSession(StartSessionIn{MissionID: m.ID, MemberID: tc.member, AccountID: tc.account, Adapter: tc.adapter, Inject: tc.inject})
			if err == nil {
				t.Fatal("invalid reference accepted")
			}
			afterSessions, err := f.svc.Store.ListSessions()
			if err != nil {
				t.Fatal(err)
			}
			afterEvents, err := f.svc.Store.EventsSince(0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			if len(afterSessions) != len(sessions) || len(afterEvents) != len(events) {
				t.Fatal("invalid reference mutated catalog")
			}
			after, err := os.ReadFile(f.launches)
			if err != nil || string(after) != string(before) {
				t.Fatalf("child executed: %q %v", after, err)
			}
		})
	}
}

func TestCreateMissionMissingReferencesDoesNotMutate(t *testing.T) {
	for _, kind := range []string{"workspace", "squad"} {
		t.Run(kind, func(t *testing.T) {
			f := referenceSetup(t)
			before, err := f.svc.Store.ListMissions("")
			if err != nil {
				t.Fatal(err)
			}
			seq, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			workspaceID, squadID := f.workspace.ID, f.mission.SquadID
			if kind == "workspace" {
				workspaceID = "missing"
			} else {
				squadID = "missing"
			}
			if _, err := f.svc.CreateMission(workspaceID, "invalid", "", squadID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("missing %s: %v", kind, err)
			}
			after, err := f.svc.Store.ListMissions("")
			if err != nil {
				t.Fatal(err)
			}
			afterSeq, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) || seq != afterSeq {
				t.Fatal("invalid mission mutated catalog")
			}
		})
	}
}

func TestNormalizedSnapshotMemberPersistence(t *testing.T) {
	for _, tc := range []struct {
		name, explicit, want string
		members              []domain.Member
		generic              bool
	}{
		{"last maestro", "", "last", []domain.Member{{ID: "first", Maestro: true}, {ID: "last", Maestro: true}}, true},
		{"first fallback", "", "first", []domain.Member{{ID: "first"}, {ID: "last"}}, true},
		{"explicit", "first", "first", []domain.Member{{ID: "first"}, {ID: "last", Maestro: true}}, true},
		{"default injection", "", "last", []domain.Member{{ID: "first"}, {ID: "last", Maestro: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := referenceSetup(t)
			m := f.mission
			m.SquadSnapshot.Members = tc.members
			if err := f.svc.Store.PutMission(m); err != nil {
				t.Fatal(err)
			}
			var inject *bool
			if tc.generic {
				off := false
				inject = &off
			}
			sess, err := f.svc.StartSession(StartSessionIn{MissionID: m.ID, MemberID: tc.explicit, Inject: inject})
			if err != nil {
				t.Fatal(err)
			}
			readyProcess(t, f.svc, sess)
			live, ok := f.svc.Session.Snapshot(sess.ID)
			if !ok || live.MemberID != tc.want || sess.MemberID != tc.want {
				t.Fatalf("returned/live IDs: %q %q", sess.MemberID, live.MemberID)
			}
			stored, err := f.svc.Store.GetSession(sess.ID)
			if err != nil || stored.MemberID != tc.want {
				t.Fatalf("stored: %+v %v", stored, err)
			}
			var seq int
			var name, path string
			if err := f.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.StopSession(sess.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.Store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reloaded, err := reopened.GetSession(sess.ID)
			if err != nil || reloaded.MemberID != tc.want {
				t.Fatalf("reloaded: %+v %v", reloaded, err)
			}

		})
	}
}

func TestWorkspaceRootErrorPreservesCause(t *testing.T) {
	f := referenceSetup(t)
	w := f.workspace
	w.Root = w.Root + "/missing"
	if err := f.svc.Store.PutWorkspace(w); err != nil {
		t.Fatal(err)
	}
	_, err := f.start()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost filesystem cause: %v", err)
	}
}

func TestSnapshotInheritanceAndHistoricalLaunch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  []domain.Member
		snapshot domain.Member
		deleted  bool
	}{
		{name: "same ID inherits", current: []domain.Member{{ID: "history", CLI: "codex", AccountID: "fixture-account"}}, snapshot: domain.Member{ID: "history", Maestro: true}},
		{name: "snapshot wins", current: []domain.Member{{ID: "history", CLI: "claude"}}, snapshot: domain.Member{ID: "history", CLI: "codex", Maestro: true}},
		{name: "different maestro cannot supply account", current: []domain.Member{{ID: "replacement", CLI: "claude", AccountID: "foreign", Maestro: true}}, snapshot: domain.Member{ID: "history", CLI: "codex", Maestro: true}},
		{name: "deleted squad", snapshot: domain.Member{ID: "history", CLI: "codex", Maestro: true}, deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := referenceSetup(t)
			m := f.mission
			m.SquadSnapshot.Members = []domain.Member{tc.snapshot}
			if err := f.svc.Store.PutMission(m); err != nil {
				t.Fatal(err)
			}
			q := m.SquadSnapshot
			q.ID = m.SquadID
			q.Members = tc.current
			wantAccount := ""
			for i := range q.Members {
				if q.Members[i].AccountID == "fixture-account" {
					q.Members[i].AccountID = f.account.ID
					wantAccount = f.account.ID
				}
			}
			if err := f.svc.Store.PutSquad(q); err != nil {
				t.Fatal(err)
			}
			if tc.deleted {
				sqlExec(t, f.db, "DELETE FROM squads")
			}
			off := false
			sess, err := f.svc.StartSession(StartSessionIn{MissionID: m.ID, Inject: &off})
			if err != nil {
				t.Fatal(err)
			}
			readyProcess(t, f.svc, sess)
			if sess.MemberID != "history" || sess.Adapter != "codex" || sess.AccountID != wantAccount {
				t.Fatalf("unexpected binding: %+v", sess)
			}
			var seq int
			var name, path string
			if err := f.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.StopSession(sess.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.Store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			stored, err := reopened.GetSession(sess.ID)
			if err != nil || stored.MemberID != "history" {
				t.Fatalf("reloaded: %+v %v", stored, err)
			}
		})
	}
}

func TestReferenceStorageFailurePreservesCause(t *testing.T) {
	for _, table := range []string{"squads", "accounts"} {
		t.Run(table, func(t *testing.T) {
			f := referenceSetup(t)
			if table == "squads" {
				sqlExec(t, f.db, "DROP TABLE squads")
			} else {
				sqlExec(t, f.db, "DROP TABLE accounts")
			}
			_, err := f.start()
			if err == nil || errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrInvalidReference) {
				t.Fatalf("storage error disguised: %v", err)
			}
			if errors.Unwrap(err) == nil {
				t.Fatalf("storage cause lost: %v", err)
			}
			if _, err := os.Stat(f.launches); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected child artifact: %v", err)
			}
		})
	}
}

func TestMissingSessionReferencesAndCWD(t *testing.T) {
	for _, kind := range []string{"mission", "empty mission", "workspace", "root missing", "root file", "member", "account"} {
		t.Run(kind, func(t *testing.T) {
			f := referenceSetup(t)
			off := false
			in := StartSessionIn{MissionID: f.mission.ID, Inject: &off}
			switch kind {
			case "mission":
				in.MissionID = "missing"
			case "empty mission":
				in.MissionID = ""
			case "workspace":
				sqlExec(t, f.db, "DELETE FROM workspaces")
			case "root missing":
				w := f.workspace
				w.Root += "/missing"
				if err := f.svc.Store.PutWorkspace(w); err != nil {
					t.Fatal(err)
				}
			case "root file":
				w := f.workspace
				w.Root += "/file"
				if err := os.WriteFile(w.Root, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := f.svc.Store.PutWorkspace(w); err != nil {
					t.Fatal(err)
				}
			case "member":
				in.MemberID = "missing"
			case "account":
				in.AccountID = "missing"
			}
			before, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.StartSession(in); err == nil {
				t.Fatal("invalid reference accepted")
			}
			after, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			sessions, err := f.svc.Store.ListSessions()
			if err != nil {
				t.Fatal(err)
			}
			if before != after || len(sessions) != 0 {
				t.Fatal("invalid reference persisted")
			}
			if _, err := os.Stat(f.launches); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("child artifact: %v", err)
			}
		})
	}
}

func TestMatchingAccountAdapterOverride(t *testing.T) {
	f := referenceSetup(t)
	other, err := f.svc.CreateAccount("synthetic other", "codex")
	if err != nil {
		t.Fatal(err)
	}
	m := f.mission
	m.SquadSnapshot.Members = []domain.Member{{ID: "history", CLI: "claude", AccountID: "missing-original"}}
	if err := f.svc.Store.PutMission(m); err != nil {
		t.Fatal(err)
	}
	off := false
	sess, err := f.svc.StartSession(StartSessionIn{MissionID: m.ID, AccountID: other.ID, Adapter: "codex", Inject: &off})
	if err != nil {
		t.Fatal(err)
	}
	readyProcess(t, f.svc, sess)
	if sess.MemberID != "history" || sess.AccountID != other.ID || sess.Adapter != "codex" {
		t.Fatalf("override: %+v", sess)
	}
}
