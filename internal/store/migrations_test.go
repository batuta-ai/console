package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Historical DDL is deliberately independent of the migration implementation.
const legacySchema = `
CREATE TABLE workspaces(id TEXT PRIMARY KEY,name TEXT NOT NULL,root TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL);
CREATE TABLE squads(id TEXT PRIMARY KEY,name TEXT NOT NULL,purpose TEXT NOT NULL,version INTEGER NOT NULL,draft INTEGER NOT NULL,members TEXT NOT NULL);
CREATE TABLE missions(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,title TEXT NOT NULL,squad_id TEXT NOT NULL,snapshot TEXT NOT NULL,status TEXT NOT NULL,attention TEXT,created_at TEXT NOT NULL);
CREATE TABLE accounts(id TEXT PRIMARY KEY,label TEXT NOT NULL,provider TEXT NOT NULL,adapter TEXT NOT NULL,isolation TEXT NOT NULL,connected INTEGER NOT NULL,session_cap INTEGER NOT NULL,auth_method TEXT NOT NULL,vault_ref TEXT NOT NULL DEFAULT '');
CREATE TABLE sessions(id TEXT PRIMARY KEY,mission_id TEXT NOT NULL,workspace_id TEXT NOT NULL,member_id TEXT,account_id TEXT,adapter TEXT NOT NULL,argv TEXT NOT NULL,process TEXT NOT NULL,work TEXT NOT NULL,owner TEXT NOT NULL,owner_client TEXT,pid INTEGER,cols INTEGER,rows INTEGER,batuta INTEGER NOT NULL,skills_version TEXT,created_at TEXT NOT NULL,ended_at TEXT);
CREATE TABLE events(seq INTEGER PRIMARY KEY AUTOINCREMENT,at TEXT NOT NULL,kind TEXT NOT NULL,payload TEXT NOT NULL);`

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func execSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}
func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func rawRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
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
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			t.Fatal(err)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestLegacyMigrationLossless(t *testing.T) {
	for _, variant := range []string{"neither", "origin", "both"} {
		t.Run(variant, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			db := rawDB(t, path)
			execSQL(t, db, legacySchema)
			execSQL(t, db, `INSERT INTO workspaces VALUES('w','Name','/old/root','2020-01-02T03:04:05.123456789-03:00');
 INSERT INTO squads VALUES('q','Squad','Purpose',7,1,'[ {"id":"member","skills":["a","b"]} ]');
 INSERT INTO missions VALUES('m','missing-workspace','Title','q','{ "name":"snapshot", "version":3 }','open','attention','2020-02-03T04:05:06Z');
 INSERT INTO accounts VALUES('a','Label','provider','codex','isolated',1,9,'oauth','/old/vault');
 INSERT INTO sessions VALUES('s','m','w','member','a','codex','[ "cmd", "--flag" ]','exited','interactive','tui','client',123,120,50,1,'v-old','2020-01-02T03:04:05Z','2020-01-02T04:04:05Z');
 INSERT INTO events VALUES(42,'2020-01-02T03:04:05Z','old.kind','{ "value": [1,2] }');`)
			if variant != "neither" {
				execSQL(t, db, `ALTER TABLE squads ADD COLUMN origin TEXT NOT NULL DEFAULT 'custom'; UPDATE squads SET origin='batuta'`)
			}
			if variant == "both" {
				execSQL(t, db, `ALTER TABLE missions ADD COLUMN brief TEXT NOT NULL DEFAULT ''; UPDATE missions SET brief='Existing brief'`)
			}
			queries := map[string]string{}
			before := map[string][][]any{}
			for _, table := range []string{"workspaces", "squads", "missions", "accounts", "sessions", "events"} {
				cols, err := db.Query("SELECT * FROM " + table + " LIMIT 0")
				if err != nil {
					t.Fatal(err)
				}
				names, err := cols.Columns()
				cols.Close()
				if err != nil {
					t.Fatal(err)
				}
				queries[table] = "SELECT " + strings.Join(names, ",") + " FROM " + table
				before[table] = rawRows(t, db, queries[table])
			}
			for i := 0; i < 2; i++ {
				s, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := version(t, s.db); got != 5 {
					t.Errorf("schema version = %d, want 5", got)
				}
				for table, query := range queries {
					if got := rawRows(t, s.db, query); !reflect.DeepEqual(got, before[table]) {
						t.Errorf("%s changed: %v -> %v", table, before[table], got)
					}
				}
				var origin, brief string
				if err := s.db.QueryRow("SELECT origin FROM squads").Scan(&origin); err != nil {
					t.Fatal(err)
				}
				if err := s.db.QueryRow("SELECT brief FROM missions").Scan(&brief); err != nil {
					t.Fatal(err)
				}
				wantOrigin, wantBrief := "custom", ""
				if variant != "neither" {
					wantOrigin = "batuta"
				}
				if variant == "both" {
					wantBrief = "Existing brief"
				}
				if origin != wantOrigin || brief != wantBrief {
					t.Errorf("defaults: %q %q", origin, brief)
				}
				s.Close()
			}
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ev, err := s.AppendEvent("new", nil)
			if err != nil || ev.Seq != 43 {
				t.Fatalf("sequence: %+v %v", ev, err)
			}
		})
	}
}
func TestEmptyMigration(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := version(t, s.db); got != 5 {
		t.Fatalf("version %d", got)
	}
}
func TestMigrationFailureRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.db")
	db := rawDB(t, path)
	execSQL(t, db, legacySchema)
	execSQL(t, db, `INSERT INTO squads VALUES('q','Keep','purpose',5,0,'[]'); DROP TABLE missions; CREATE VIEW missions AS SELECT 'blocked' AS id; PRAGMA user_version=1;`)
	s, err := Open(path)
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "migration 2") || !strings.Contains(err.Error(), "brief") {
		t.Fatalf("expected contextual migration failure, got %v", err)
	}
	if v := version(t, db); v != 1 {
		t.Fatalf("version advanced: %d", v)
	}
	if _, err := db.Exec("SELECT origin FROM squads"); err == nil {
		t.Fatal("failed migration retained origin DDL")
	}
	if got := rawRows(t, db, "SELECT * FROM squads"); !reflect.DeepEqual(got, [][]any{{"q", "Keep", "purpose", int64(5), int64(0), "[]"}}) {
		t.Fatal(got)
	}
	execSQL(t, db, `DROP VIEW missions; CREATE TABLE missions(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,title TEXT NOT NULL,squad_id TEXT NOT NULL,snapshot TEXT NOT NULL,status TEXT NOT NULL,attention TEXT,created_at TEXT NOT NULL);`)
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if version(t, s.db) != 5 {
		t.Fatal("recovery did not advance")
	}
}
func TestFutureVersionUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db := rawDB(t, path)
	execSQL(t, db, `CREATE TABLE future(value TEXT); INSERT INTO future VALUES('keep'); PRAGMA user_version=99;`)
	before := rawRows(t, db, "SELECT * FROM sqlite_schema ORDER BY name")
	s, err := Open(path)
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("expected future version refusal: %v", err)
	}
	if version(t, db) != 99 || !reflect.DeepEqual(before, rawRows(t, db, "SELECT * FROM sqlite_schema ORDER BY name")) {
		t.Fatal("future schema modified")
	}
	if got := fmt.Sprint(rawRows(t, db, "SELECT * FROM future")); got != "[[keep]]" {
		t.Fatal(got)
	}
}

func TestSchema2RecoveryMetadataMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema2.db")
	db := rawDB(t, path)
	execSQL(t, db, legacySchema)
	execSQL(t, db, `ALTER TABLE squads ADD COLUMN origin TEXT NOT NULL DEFAULT 'custom'; ALTER TABLE missions ADD COLUMN brief TEXT NOT NULL DEFAULT ''; PRAGMA user_version=2;
 INSERT INTO sessions VALUES('s','m','w','member','account','codex','[ "cmd", "arg" ]','running','interactive','web','client',123,120,50,1,'v1','2020-01-02T03:04:05Z',NULL);
 INSERT INTO events VALUES(42,'2020-01-02T03:04:05Z','old.event','{ "keep": true }');`)
	const oldColumns = "id,mission_id,workspace_id,member_id,account_id,adapter,argv,process,work,owner,owner_client,pid,cols,rows,batuta,skills_version,created_at,ended_at"
	before := rawRows(t, db, "SELECT "+oldColumns+" FROM sessions")
	events := rawRows(t, db, "SELECT * FROM events")
	for range 2 {
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if version(t, st.db) != 5 {
			t.Errorf("schema2 did not upgrade to 5")
		}
		if !reflect.DeepEqual(before, rawRows(t, st.db, "SELECT "+oldColumns+" FROM sessions")) || !reflect.DeepEqual(events, rawRows(t, st.db, "SELECT * FROM events")) {
			t.Fatal("migration changed history")
		}
		var identity, reason string
		err = st.db.QueryRow("SELECT process_identity,recovery_reason FROM sessions").Scan(&identity, &reason)
		if err != nil || identity != "" || reason != "" {
			t.Errorf("legacy defaults: %q %q %v", identity, reason, err)
		}
		st.Close()
	}
}

func TestRecoveryMigrationRollsBackFirstColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.db")
	db := rawDB(t, path)
	execSQL(t, db, legacySchema)
	// Conflicting second column forces failure after first ALTER, proving DDL rollback.
	execSQL(t, db, `ALTER TABLE sessions ADD COLUMN recovery_reason TEXT; PRAGMA user_version=2;`)
	before := rawRows(t, db, "SELECT * FROM sqlite_schema ORDER BY name")
	st, err := Open(path)
	if st != nil {
		st.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "migration 3") {
		t.Fatalf("want migration 3 error: %v", err)
	}
	if version(t, db) != 2 || !reflect.DeepEqual(before, rawRows(t, db, "SELECT * FROM sqlite_schema ORDER BY name")) {
		t.Fatal("failed migration leaked DDL/version")
	}
	execSQL(t, db, "ALTER TABLE sessions DROP COLUMN recovery_reason")
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if version(t, st.db) != 5 {
		t.Fatal("retry did not migrate")
	}
}
