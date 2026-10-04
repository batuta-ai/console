package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestMissionBindingSchema(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutMission(domain.Mission{ID: "legacy", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	var path, digest, delivery string
	if err := s.db.QueryRow(`SELECT plan_path,plan_digest,delivery_id FROM missions WHERE id='legacy'`).Scan(&path, &digest, &delivery); err != nil {
		t.Fatalf("missing durable mission binding: %v", err)
	}
	if path != "" || digest != "" || delivery != "" {
		t.Fatal("legacy mission adopted a binding")
	}
}

func TestMissionBindingAtomicPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(domain.Workspace{ID: "w", Root: "/workspace", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.PutMission(domain.Mission{ID: id, WorkspaceID: "w", Title: "original", CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	binding := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/workspace", CanonicalRoot: "/workspace", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "core-digest", DeliveryID: "run", OpenedDigest: "opened-digest"}
	execSQL(t, s.db, `CREATE TRIGGER reject_binding_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'event refused'); END`)
	if err := s.BindMission("a", binding); err == nil {
		t.Fatal("event failure accepted")
	}
	m, err := s.GetMission("a")
	if err != nil || m.Binding != nil {
		t.Fatalf("rollback: %+v %v", m, err)
	}
	execSQL(t, s.db, `DROP TRIGGER reject_binding_event`)
	if err := s.BindMission("a", binding); err != nil {
		t.Fatal(err)
	}
	if err := s.BindMission("a", binding); err != nil {
		t.Fatal(err)
	}
	if err := s.BindMission("b", binding); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatalf("stolen delivery: %v", err)
	}
	replacement := binding
	replacement.DeliveryID = "other"
	if err := s.BindMission("a", replacement); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatalf("replaced delivery: %v", err)
	}
	// A pre-binding mission snapshot must not erase the binding when writing metadata.
	if err := s.PutMission(domain.Mission{ID: "a", WorkspaceID: "w", Title: "updated", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err = s.GetMission("a")
	if err != nil || m.Binding == nil || *m.Binding != binding || m.Title != "updated" {
		t.Fatalf("reopen: %+v %v", m, err)
	}
	missions, err := s.ListMissions("w")
	if err != nil || len(missions) != 2 {
		t.Fatalf("list %v %v", missions, err)
	}
	for _, m := range missions {
		if m.ID == "a" && (m.Binding == nil || *m.Binding != binding) {
			t.Fatalf("list lost binding: %+v", m)
		}
	}
	events, err := s.EventsSince(0, 100)
	if err != nil || len(events) != 1 {
		t.Fatalf("atomic/idempotent events: %v %v", events, err)
	}
	for _, key := range []string{"mission_id", "workspace_id", "workspace_root", "plan_path", "plan_slug", "plan_digest", "delivery_id", "opened_digest"} {
		if events[0].Payload[key] == nil {
			t.Fatalf("event lacks %s: %v", key, events)
		}
	}
}

func TestMissionBindingConcurrentOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutWorkspace(domain.Workspace{ID: "w", Root: "/workspace", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := s.PutMission(domain.Mission{ID: id, WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	binding := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/workspace", CanonicalRoot: "/workspace", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "digest", DeliveryID: "run", OpenedDigest: "opened"}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- s.BindMission("a", binding) }()
	go func() { <-start; results <- other.BindMission("b", binding) }()
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful owners=%d", successes)
	}
	events, err := s.EventsSince(0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events %v %v", events, err)
	}
}

func TestMissionBindingSchema3Lossless(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db := rawDB(t, path)
	execSQL(t, db, legacySchema)
	execSQL(t, db, `ALTER TABLE squads ADD COLUMN origin TEXT NOT NULL DEFAULT 'custom';
ALTER TABLE missions ADD COLUMN brief TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN process_identity TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT '';
PRAGMA user_version=3;
INSERT INTO workspaces VALUES('w','Name','/old/root','2020-01-02T03:04:05.123456789-03:00');
INSERT INTO squads VALUES('q','Squad','Purpose',7,1,'[ {"id":"member","skills":["a","b"]} ]','batuta');
INSERT INTO missions VALUES('m','w','Title','q','{ "name":"snapshot", "version":3 }','open','attention','2020-02-03T04:05:06Z','Historical brief');
INSERT INTO accounts VALUES('a','Label','provider','codex','isolated',1,9,'oauth','/old/vault');
INSERT INTO sessions VALUES('s','m','w','member','a','codex','[ "cmd", "--flag" ]','exited','interactive','tui','client',123,120,50,1,'v-old','2020-01-02T03:04:05Z','2020-01-02T04:04:05Z','incarnation','reason');
INSERT INTO events VALUES(42,'2020-01-02T03:04:05Z','old.kind','{ "value": [1,2] }');`)
	queries := map[string]string{}
	before := map[string][][]any{}
	for _, table := range []string{"workspaces", "squads", "missions", "accounts", "sessions", "events"} {
		rows, err := db.Query("SELECT * FROM " + table + " LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		names, err := rows.Columns()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		queries[table] = "SELECT " + strings.Join(names, ",") + " FROM " + table
		before[table] = rawRows(t, db, queries[table])
	}
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for table, query := range queries {
			if !reflect.DeepEqual(before[table], rawRows(t, s.db, query)) {
				t.Fatalf("migration changed %s", table)
			}
		}
		m, err := s.GetMission("m")
		if err != nil || m.Binding != nil {
			t.Fatalf("legacy binding %+v %v", m, err)
		}
		if version(t, s.db) != 4 {
			t.Fatal("schema not advanced")
		}
		s.Close()
	}
}

func TestMissionBindingWriteRollbackAndMetadata(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutWorkspace(domain.Workspace{ID: "w", Root: "/root", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	original := domain.Mission{ID: "m", WorkspaceID: "w", Title: "keep", Brief: "brief", Attention: "attention", Status: "ready", SquadSnapshot: domain.Squad{Name: "snapshot"}, CreatedAt: domain.Now()}
	if err := s.PutMission(original); err != nil {
		t.Fatal(err)
	}
	b := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/root", CanonicalRoot: "/root", PlanPath: ".batuta/plans/a.md", PlanSlug: "a", PlanDigest: "a"}
	if err := s.BindMission("m", b); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetMission("m")
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsSince(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s.db, `CREATE TRIGGER reject_binding_write BEFORE UPDATE OF plan_path ON missions BEGIN SELECT RAISE(ABORT,'write refused'); END`)
	b.PlanPath = ".batuta/plans/b.md"
	b.PlanSlug = "b"
	b.PlanDigest = "b"
	if err := s.BindMission("m", b); err == nil {
		t.Fatal("write failure accepted")
	}
	after, err := s.GetMission("m")
	if err != nil {
		t.Fatal(err)
	}
	afterEvents, err := s.EventsSince(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(events, afterEvents) {
		t.Fatal("failed mutation changed state")
	}
	after.Binding = nil
	if !reflect.DeepEqual(original, after) {
		t.Fatalf("binding changed metadata: %#v %#v", original, after)
	}
	execSQL(t, s.db, `DROP TRIGGER reject_binding_write`)
	moved := original
	moved.WorkspaceID = "foreign"
	if err := s.PutMission(moved); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatalf("moved bound mission: %v", err)
	}
}
