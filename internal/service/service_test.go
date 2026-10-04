package service

import (
	"path/filepath"
	"strings"
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

func TestSessionPrompt(t *testing.T) {
	got := sessionPrompt(true, "Add a health check")
	if !strings.Contains(got, "batuta-plan") || !strings.Contains(got, "Add a health check") {
		t.Fatalf("%q", got)
	}
	if plain := sessionPrompt(false, "  just the goal  "); plain != "  just the goal  " {
		t.Fatalf("%q", plain)
	}
	for _, inject := range []bool{false, true} {
		if blank := sessionPrompt(inject, " \t\n "); blank != "" {
			t.Fatalf("blank goal: %q", blank)
		}
	}
}

func TestStartSessionDefaultInject(t *testing.T) {
	root := t.TempDir()
	installedSkillFixture(t, root, "batuta", "batuta-review")
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
	msn, err := svc.CreateMission(ws.ID, "Checkout", "Ship the checkout form", sq.ID)
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

func TestConnectBindsVacantBatutaMembers(t *testing.T) {
	svc := testSvc(t)
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	ciclo, err := svc.Store.GetSquad("sq_batuta_ciclo")
	if err != nil || !ciclo.Draft {
		t.Fatalf("seeded ciclo should be draft: %+v %v", ciclo, err)
	}
	codex, err := svc.CreateAccount("Codex", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(codex.ID); err != nil {
		t.Fatal(err)
	}
	backend, err := svc.Store.GetSquad("sq_batuta_backend")
	if err != nil || backend.Draft || backend.Members[0].AccountID != codex.ID {
		t.Fatalf("unique connected Codex should complete structural binding: %+v %v", backend, err)
	}
	ciclo, err = svc.Store.GetSquad("sq_batuta_ciclo")
	if err != nil || !ciclo.Draft {
		t.Fatalf("ciclo still needs Claude: %+v %v", ciclo, err)
	}
	claude, err := svc.CreateAccount("Claude", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(claude.ID); err != nil {
		t.Fatal(err)
	}
	ciclo, err = svc.Store.GetSquad("sq_batuta_ciclo")
	if err != nil || ciclo.Draft {
		t.Fatalf("connected accounts should complete structural binding: %+v %v", ciclo, err)
	}
	for _, member := range ciclo.Members {
		want := codex.ID
		if member.CLI == "claude" {
			want = claude.ID
		}
		if member.AccountID != want {
			t.Fatalf("connected member not bound: %+v", member)
		}
	}
	v := svc.ValidateDraft(ciclo)
	if v.Ready || v.Tone != "warning" {
		t.Fatalf("unverified accounts must not be ready: %+v", v)
	}
}

func TestRenameAccountKeepsAdapter(t *testing.T) {
	svc := testSvc(t)
	a, err := svc.CreateAccount("Codex Work", "codex")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.RenameAccount(a.ID, "  trabalho  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "trabalho" || got.Adapter != "codex" || got.ID != a.ID {
		t.Fatalf("%+v", got)
	}
	if _, err := svc.RenameAccount(a.ID, " "); err == nil {
		t.Fatal("empty label")
	}
}

func TestAmbiguousAccountsDoNotBind(t *testing.T) {
	svc := testSvc(t)
	a, err := svc.CreateAccount("one", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateAccount("two", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	q, err := svc.Store.GetSquad("sq_batuta_backend")
	if err != nil || !q.Draft {
		t.Fatalf("two Codex accounts must not auto-bind: %+v %v", q, err)
	}
	for _, m := range q.Members {
		if m.AccountID != "" {
			t.Fatalf("vacant member bound %q", m.AccountID)
		}
	}
}

func TestEnsureCatalogBindsExistingAccount(t *testing.T) {
	svc := testSvc(t)
	a, err := svc.CreateAccount("Codex", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	q, err := svc.Store.GetSquad("sq_batuta_backend")
	if err != nil || q.Draft || q.Members[0].AccountID != a.ID {
		t.Fatalf("seed should bind unique Codex: %+v %v", q, err)
	}
}
