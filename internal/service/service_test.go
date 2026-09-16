package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/plugins/limits"
)

func testSvc(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := New(st, session.New(t.TempDir()), limits.New(nil))
	svc.Home = t.TempDir()
	return svc
}

func TestEnsureBatutaCatalogIdempotent(t *testing.T) {
	svc := testSvc(t)
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Store.ListSquads()
	if err != nil || len(first) != len(batuta.BuiltInSquads()) {
		t.Fatalf("seeded %d want %d err %v", len(first), len(batuta.BuiltInSquads()), err)
	}
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Store.ListSquads()
	if err != nil || len(again) != len(first) {
		t.Fatalf("idempotent %d -> %d", len(first), len(again))
	}
	got, err := svc.Store.GetSquad("sq_batuta_ciclo")
	if err != nil || got.Origin != domain.OriginBatuta || got.Version != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSaveSquadPreservesBatutaOrigin(t *testing.T) {
	svc := testSvc(t)
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	q, err := svc.Store.GetSquad("sq_batuta_ciclo")
	if err != nil {
		t.Fatal(err)
	}
	q.Purpose = "updated"
	q.Origin = ""
	saved, err := svc.SaveSquad(q)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Origin != domain.OriginBatuta || saved.Version != 2 {
		t.Fatalf("%+v", saved)
	}
}

func TestStartSessionDefaultInject(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "batuta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("#\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATUTA_SKILLS", root)

	svc := testSvc(t)
	ws, err := svc.CreateWorkspace("shop", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acct, err := svc.CreateAccount("Codex Work", "codex")
	if err != nil {
		t.Fatal(err)
	}
	sq, err := svc.SaveSquad(domain.Squad{Name: "Web", Members: []domain.Member{{
		Role: "Maestro", CLI: "codex", AccountID: acct.ID, Maestro: true, Skills: []string{"batuta", "batuta-review"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	msn, err := svc.CreateMission(ws.ID, "Checkout", sq.ID)
	if err != nil {
		t.Fatal(err)
	}
	on, err := svc.StartSession(StartSessionIn{MissionID: msn.ID, Argv: []string{"/bin/sleep", "8"}, Owner: "tui", Client: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopSession(on.ID)
	if !on.BatutaInjected {
		t.Fatalf("default inject should enable when skills exist: %+v", on)
	}
	off := false
	generic, err := svc.StartSession(StartSessionIn{MissionID: msn.ID, Argv: []string{"/bin/sleep", "8"}, Owner: "web", Client: "c2", Inject: &off})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.StopSession(generic.ID)
	if generic.BatutaInjected {
		t.Fatalf("explicit false must skip inject: %+v", generic)
	}
}
