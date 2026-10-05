package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
)

func approvalStoreFixture(t *testing.T) (*Store, string, domain.PlanIdentity, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkspace(domain.Workspace{ID: "w", Root: "/root", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	b := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/root", CanonicalRoot: "/root", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "core"}
	if err := s.BindMission("m", b); err != nil {
		t.Fatal(err)
	}
	content := []byte("original\r\n\xff")
	return s, path, domain.PlanIdentity{MissionID: "m", Binding: b, Version: domain.PlanVersion(content)}, content
}
func TestPlanApprovalAtomicDurable(t *testing.T) {
	s, path, id, body := approvalStoreFixture(t)
	ctx := context.Background()
	read := func() ([]byte, error) { return body, nil }
	execSQL(t, s.db, `CREATE TRIGGER refuse_approval BEFORE INSERT ON events WHEN NEW.kind='plan.approved' BEGIN SELECT RAISE(ABORT,'event refused'); END`)
	if _, err := s.ApprovePlan(ctx, id, read); err == nil {
		t.Fatal("event failure accepted")
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM plan_approvals`).Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan snapshot", n, err)
	}
	execSQL(t, s.db, `DROP TRIGGER refuse_approval`)
	receipt, err := s.ApprovePlan(ctx, id, read)
	if err != nil {
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
	got, err := s.GetPlanApproval(ctx, "m", "w", receipt.ID)
	if err != nil || got.Identity != id || !bytes.Equal(got.Content, body) || got.ApprovedAt.IsZero() {
		t.Fatalf("reload %+v %v", got, err)
	}
	if _, err := s.GetPlanApproval(ctx, "other", "w", receipt.ID); err == nil {
		t.Fatal("foreign receipt")
	}
	execSQL(t, s.db, `CREATE TRIGGER refuse_snapshot BEFORE INSERT ON plan_approvals BEGIN SELECT RAISE(ABORT,'snapshot refused'); END`)
	next := id
	next.Version = domain.PlanVersion([]byte("second"))
	if _, err := s.ApprovePlan(ctx, next, func() ([]byte, error) { return []byte("second"), nil }); err == nil {
		t.Fatal("storage failure accepted")
	}
	events, err := s.EventsSince(0, 100)
	if err != nil || len(events) != 2 {
		t.Fatal("non atomic events", events, err)
	}
}
func TestPlanApprovalConcurrentOwnership(t *testing.T) {
	s, path, id, body := approvalStoreFixture(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan domain.PlanApproval, 2)
	errs := make(chan error, 2)
	for _, st := range []*Store{s, other} {
		go func() {
			<-start
			r, e := st.ApprovePlan(context.Background(), id, func() ([]byte, error) { return body, nil })
			results <- r
			errs <- e
		}()
	}
	close(start)
	a, b := <-results, <-results
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if a.ID != b.ID || a.ID == "" {
		t.Fatal("duplicate receipt")
	}
	events, err := s.EventsSince(0, 100)
	if err != nil || len(events) != 2 {
		t.Fatal("duplicate events", err)
	}
	changed := id.Binding
	changed.PlanSlug = "beta"
	changed.PlanPath = ".batuta/plans/beta.md"
	if err := other.BindMission("m", changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePlan(context.Background(), id, func() ([]byte, error) { return body, nil }); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatalf("stale preflight: %v", err)
	}
}

func TestPlanApprovalMigrationDoesNotInventEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := rawDB(t, path)
	execSQL(t, db, legacySchema)
	execSQL(t, db, `INSERT INTO workspaces VALUES('w','Name','/root','2020-01-01T00:00:00Z'); INSERT INTO missions VALUES('m','w','Keep','s','{}','open','','2020-01-01T00:00:00Z')`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.GetMission("m")
	if err != nil || m.Title != "Keep" || m.Binding != nil {
		t.Fatal("legacy lost", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM plan_approvals`).Scan(&n); err != nil || n != 0 {
		t.Fatal("invented evidence", n, err)
	}
	if version(t, s.db) != 5 {
		t.Fatal("schema not versioned")
	}
}

func TestPlanApprovalCancellationAndImmutability(t *testing.T) {
	s, _, id, body := approvalStoreFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := s.ApprovePlan(ctx, id, func() ([]byte, error) { cancel(); return body, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled %v", err)
	}
	if _, err := s.FindPlanApproval(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("canceled persisted", err)
	}
	receipt, err := s.ApprovePlan(context.Background(), id, func() ([]byte, error) { return body, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE plan_approvals SET content=x'00'`, `DELETE FROM plan_approvals`} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("mutable receipt")
		}
	}
	got, err := s.GetPlanApproval(context.Background(), "m", "w", receipt.ID)
	if err != nil || !bytes.Equal(got.Content, body) {
		t.Fatal("changed snapshot", err)
	}
}

// A real external writer edits after the bounded native read and before commit.
// The channels declare that ordering; no timing assumption or native parser mock.
func TestPlanApprovalExternalWriterAfterValidation(t *testing.T) {
	s, _, id, _ := approvalStoreFixture(t)
	root := t.TempDir()
	path := ".batuta/plans/alpha.md"
	if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("# Plan — Alpha\n**Status:** proposed\n- [ ] 1. Work — data/high\n  Accept: verified\n\n## Decisions and context\nOriginal.\n")
	full := filepath.Join(root, path)
	if err := os.WriteFile(full, original, 0600); err != nil {
		t.Fatal(err)
	}
	p, _, err := batuta.ExactPlanContent(root, path)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := batuta.CanonicalWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	id.Binding = domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: root, CanonicalRoot: canonical, PlanSlug: "alpha", PlanPath: path, PlanDigest: p.Set.Digest}
	id.Version = domain.PlanVersion(original)
	if err := s.BindMission("m", id.Binding); err != nil {
		t.Fatal(err)
	}
	validated := make(chan struct{})
	written := make(chan error, 1)
	edited := bytes.Replace(original, []byte("Original."), []byte("External edit."), 1)
	go func() { <-validated; written <- os.WriteFile(full, edited, 0600) }()
	receipt, err := s.ApprovePlan(context.Background(), id, func() ([]byte, error) {
		parsed, body, err := batuta.ExactPlanContent(root, path)
		if err != nil {
			return nil, err
		}
		if parsed.Set.Digest != id.Binding.PlanDigest || domain.PlanVersion(body) != id.Version {
			return nil, domain.ErrPlanStale
		}
		close(validated)
		if err := <-written; err != nil {
			return nil, err
		}
		return body, nil
	})
	if err != nil || !bytes.Equal(receipt.Content, original) {
		t.Fatal("approved writer bytes", err)
	}
	source, err := os.ReadFile(full)
	if err != nil || !bytes.Equal(source, edited) {
		t.Fatal("overwrote external edit", err)
	}
	current := id
	current.Version = domain.PlanVersion(edited)
	if _, err := s.FindPlanApproval(context.Background(), current); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("edit automatically approved", err)
	}
	next, err := s.ApprovePlan(context.Background(), current, func() ([]byte, error) { _, b, e := batuta.ExactPlanContent(root, path); return b, e })
	if err != nil || next.ID == receipt.ID {
		t.Fatal("new explicit receipt", err)
	}
	old, err := s.GetPlanApproval(context.Background(), "m", "w", receipt.ID)
	if err != nil || !bytes.Equal(old.Content, original) {
		t.Fatal("historical changed", err)
	}
}

func TestPlanApprovalPreservesSchema4Binding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema4.db")
	db := rawDB(t, path)
	execSQL(t, db, legacySchema+`
 ALTER TABLE squads ADD COLUMN origin TEXT NOT NULL DEFAULT 'custom';
 ALTER TABLE missions ADD COLUMN brief TEXT NOT NULL DEFAULT '';
 ALTER TABLE sessions ADD COLUMN process_identity TEXT NOT NULL DEFAULT '';
 ALTER TABLE sessions ADD COLUMN recovery_reason TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN binding_root TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN binding_canonical_root TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN plan_path TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN plan_slug TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN plan_digest TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN delivery_id TEXT NOT NULL DEFAULT '';
 ALTER TABLE missions ADD COLUMN opened_digest TEXT NOT NULL DEFAULT '';
 CREATE UNIQUE INDEX mission_delivery_owner ON missions(workspace_id,delivery_id) WHERE delivery_id <> '';
 CREATE UNIQUE INDEX mission_delivery_root_owner ON missions(binding_canonical_root,delivery_id) WHERE delivery_id <> '';
 INSERT INTO workspaces VALUES('w','Workspace','/root','2020-01-01T00:00:00Z');
 INSERT INTO missions(id,workspace_id,title,squad_id,snapshot,status,attention,created_at,binding_root,binding_canonical_root,plan_path,plan_slug,plan_digest,delivery_id,opened_digest)
 VALUES('m','w','Keep','s','{}','open','','2020-01-01T00:00:00Z','/root','/root','.batuta/plans/a.md','a','core','run','opened');
 INSERT INTO events VALUES(12,'2020-01-01T00:00:00Z','mission.bound','{}');
 PRAGMA user_version=4;`)
	before := rawRows(t, db, `SELECT * FROM missions`)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !reflect.DeepEqual(before, rawRows(t, s.db, `SELECT * FROM missions`)) {
		t.Fatal("migration changed old mission")
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM plan_approvals`).Scan(&n); err != nil || n != 0 {
		t.Fatal("migration invented approval", n, err)
	}
	events, err := s.EventsSince(0, 100)
	if err != nil || len(events) != 1 || events[0].Seq != 12 {
		t.Fatal("migration changed events", err)
	}
}

func TestPlanApprovalCannotBeReplaced(t *testing.T) {
	s, _, id, body := approvalStoreFixture(t)
	receipt, err := s.ApprovePlan(context.Background(), id, func() ([]byte, error) { return body, nil })
	if err != nil {
		t.Fatal(err)
	}
	// SQLite REPLACE can bypass a DELETE trigger unless recursive triggers are on.
	_, err = s.db.Exec(`INSERT OR REPLACE INTO plan_approvals(id,mission_id,workspace_id,identity,content,approved_at) SELECT id,mission_id,workspace_id,identity,?,approved_at FROM plan_approvals WHERE id=?`, []byte("replaced"), receipt.ID)
	if err == nil {
		t.Fatal("SQLite REPLACE overwrote immutable approved bytes")
	}
	stored, err := s.GetPlanApproval(context.Background(), "m", "w", receipt.ID)
	if err != nil || !bytes.Equal(stored.Content, body) {
		t.Fatal("snapshot changed", err)
	}
}
