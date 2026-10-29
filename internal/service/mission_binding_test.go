package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func serviceBindingFixture(t *testing.T, root, slug, delivery string) string {
	t.Helper()
	physical, err := testfixture.ResolvePath(root)
	if err != nil {
		t.Fatal(err)
	}
	root = physical
	body := []byte("# Plan — " + slug + "\n**Status:** approved\n- [ ] 1. " + slug + " — data/high\n  Accept: verified\n")
	if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
		t.Fatal(err)
	}
	path := routing.PlanPath(slug)
	if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
		t.Fatal(err)
	}
	loader, err := routing.NewPlanLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := loader.LoadPlan(slug)
	if err != nil {
		t.Fatal(err)
	}
	if delivery != "" {
		canonical, err := testfixture.ResolvePath(root)
		if err != nil {
			t.Fatal(err)
		}
		js, err := journal.Open(canonical)
		if err != nil {
			t.Fatal(err)
		}
		detail, _ := json.Marshal(map[string]string{"slug": slug, "workspace": canonical, "plan_path": plan.Path, "plan_digest": plan.Set.Digest})
		if _, err := js.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: detail, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestMissionBindingCanonicalCoreWorkspace(t *testing.T) {
	svc := setupService(t)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := testfixture.DirectoryLink(root, alias); err != nil {
		t.Fatal(err)
	}
	path := serviceBindingFixture(t, root, "alpha", "run")
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: alias, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: path, DeliveryID: "run"}); err != nil {
		t.Fatal(err)
	}
	view, err := svc.WatchMission("m", "w", "run")
	if err != nil || !view.Available {
		t.Fatalf("canonical core workspace: %+v %v", view, err)
	}
}

func TestMissionBindingPlanOnlyRootReplacement(t *testing.T) {
	svc := setupService(t)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	other := t.TempDir()
	if err := testfixture.DirectoryLink(root, alias); err != nil {
		t.Fatal(err)
	}
	path := serviceBindingFixture(t, root, "alpha", "")
	serviceBindingFixture(t, other, "alpha", "")
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: alias, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: path}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := testfixture.DirectoryLink(other, alias); err != nil {
		t.Fatal(err)
	}
	view, err := svc.WatchMission("m", "", "")
	if err != nil || len(view.Plans) != 0 || view.Message != "workspace_changed" {
		t.Fatalf("adopted replacement workspace: %+v %v", view, err)
	}
}

func TestMissionBindingServiceRollbackReassignment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, nil, nil)
	root := t.TempDir()
	alpha := serviceBindingFixture(t, root, "alpha", "")
	beta := serviceBindingFixture(t, root, "beta", "")
	if err := st.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", Title: "keep", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	original, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: alpha})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_bound_event BEFORE INSERT ON events WHEN NEW.kind='mission.bound' BEGIN SELECT RAISE(ABORT,'event refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: beta}); err == nil || !CatalogReferenceFailure(err) {
		t.Fatalf("event failure %v", err)
	}
	after, err := st.GetMission("m")
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Fatalf("rollback %+v %v", after, err)
	}
	events, err := st.EventsSince(0, 20)
	if err != nil || len(events) != 1 {
		t.Fatalf("events %v %v", events, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_bound_event`); err != nil {
		t.Fatal(err)
	}
	reassigned, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: beta})
	if err != nil || reassigned.Binding.PlanPath != beta || reassigned.Title != "keep" {
		t.Fatalf("reassignment %+v %v", reassigned, err)
	}
	events, err = st.EventsSince(0, 20)
	if err != nil || len(events) != 2 {
		t.Fatalf("events %v %v", events, err)
	}
	if _, err := svc.BindMission("missing", MissionBindingRequest{WorkspaceID: "w", PlanPath: alpha}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing mission: %v", err)
	}
}

func TestMissionBindingAliasCannotStealDelivery(t *testing.T) {
	svc := setupService(t)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := testfixture.DirectoryLink(root, alias); err != nil {
		t.Fatal(err)
	}
	path := serviceBindingFixture(t, root, "alpha", "run")
	for i, workspace := range []string{root, alias} {
		id := fmt.Sprintf("w%d", i)
		if err := svc.Store.PutWorkspace(domain.Workspace{ID: id, Root: workspace, CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := svc.Store.PutMission(domain.Mission{ID: id, WorkspaceID: id, CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.BindMission("w0", MissionBindingRequest{WorkspaceID: "w0", PlanPath: path, DeliveryID: "run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("w1", MissionBindingRequest{WorkspaceID: "w1", PlanPath: path, DeliveryID: "run"}); !errors.Is(err, domain.ErrBindingConflict) {
		t.Fatalf("alias stole same physical journal: %v", err)
	}
}
