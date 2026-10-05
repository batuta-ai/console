package store

import (
	"database/sql"
	"fmt"
)

const schemaVersion = 5

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("unsupported schema version %d (latest %d)", version, schemaVersion)
	}
	for next := version + 1; next <= schemaVersion; next++ {
		if err := s.applyMigration(next); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(version int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("migration %d begin: %w", version, err)
	}
	defer tx.Rollback()
	switch version {
	case 1:
		if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, root TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS squads (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, purpose TEXT NOT NULL, version INTEGER NOT NULL, draft INTEGER NOT NULL, members TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS missions (
  id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, title TEXT NOT NULL, squad_id TEXT NOT NULL, snapshot TEXT NOT NULL, status TEXT NOT NULL, attention TEXT, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY, label TEXT NOT NULL, provider TEXT NOT NULL, adapter TEXT NOT NULL, isolation TEXT NOT NULL, connected INTEGER NOT NULL, session_cap INTEGER NOT NULL, auth_method TEXT NOT NULL, vault_ref TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY, mission_id TEXT NOT NULL, workspace_id TEXT NOT NULL, member_id TEXT, account_id TEXT, adapter TEXT NOT NULL, argv TEXT NOT NULL, process TEXT NOT NULL, work TEXT NOT NULL, owner TEXT NOT NULL, owner_client TEXT, pid INTEGER, cols INTEGER, rows INTEGER, batuta INTEGER NOT NULL, skills_version TEXT, created_at TEXT NOT NULL, ended_at TEXT
);
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, kind TEXT NOT NULL, payload TEXT NOT NULL
);
`); err != nil {
			return fmt.Errorf("migration 1 create catalog tables: %w", err)
		}
	case 2:
		for _, column := range []struct{ table, name, definition string }{
			{"squads", "origin", "TEXT NOT NULL DEFAULT 'custom'"},
			{"missions", "brief", "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := adoptColumn(tx, column.table, column.name, column.definition); err != nil {
				return fmt.Errorf("migration 2 adopt %s.%s: %w", column.table, column.name, err)
			}
		}
	case 3:
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN process_identity TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT '';`); err != nil {
			return fmt.Errorf("migration 3 add session recovery metadata: %w", err)
		}
	case 4:
		if _, err := tx.Exec(`ALTER TABLE missions ADD COLUMN binding_root TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN binding_canonical_root TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN plan_path TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN plan_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN plan_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN delivery_id TEXT NOT NULL DEFAULT '';
ALTER TABLE missions ADD COLUMN opened_digest TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX mission_delivery_owner ON missions(workspace_id,delivery_id) WHERE delivery_id <> '';
CREATE UNIQUE INDEX mission_delivery_root_owner ON missions(binding_canonical_root,delivery_id) WHERE delivery_id <> '';`); err != nil {
			return fmt.Errorf("migration 4 add mission binding: %w", err)
		}
	case 5:
		if _, err := tx.Exec(`CREATE TABLE plan_approvals (
 id TEXT PRIMARY KEY, mission_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
 identity TEXT NOT NULL UNIQUE, content BLOB NOT NULL CHECK(length(content) BETWEEN 1 AND 1048576), approved_at TEXT NOT NULL
);
CREATE INDEX plan_approval_mission ON plan_approvals(mission_id,workspace_id);
CREATE TRIGGER plan_approval_immutable_replace BEFORE INSERT ON plan_approvals
WHEN EXISTS(SELECT 1 FROM plan_approvals WHERE id=NEW.id OR identity=NEW.identity)
BEGIN SELECT RAISE(ABORT,'immutable plan approval'); END;
CREATE TRIGGER plan_approval_immutable_update BEFORE UPDATE ON plan_approvals BEGIN SELECT RAISE(ABORT,'immutable plan approval'); END;
CREATE TRIGGER plan_approval_immutable_delete BEFORE DELETE ON plan_approvals BEGIN SELECT RAISE(ABORT,'immutable plan approval'); END;`); err != nil {
			return fmt.Errorf("migration 5 add immutable plan approvals: %w", err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("migration %d record version: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %d commit: %w", version, err)
	}
	return nil
}

func adoptColumn(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if found {
		return nil
	}
	_, err = tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}
