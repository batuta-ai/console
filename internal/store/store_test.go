package store

import (
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestWorkspaceMissionSquad(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws := domain.Workspace{ID: "ws_1", Name: "shop", Root: t.TempDir(), CreatedAt: domain.Now()}
	if err := s.PutWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	sq := domain.Squad{ID: "sq_1", Name: "Web", Purpose: "ship a web change", Version: 1, Members: []domain.Member{{ID: "m1", Role: "Maestro", CLI: "codex", AccountID: "a1", Maestro: true}}}
	if err := s.PutSquad(sq); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSquad("sq_1")
	if err != nil || got.Origin != domain.OriginCustom {
		t.Fatalf("default origin %+v %v", got, err)
	}
	sq.Origin = domain.OriginBatuta
	if err := s.PutSquad(sq); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSquad("sq_1")
	if err != nil || got.Origin != domain.OriginBatuta {
		t.Fatalf("origin roundtrip %+v %v", got, err)
	}
	msn := domain.Mission{ID: "msn_1", WorkspaceID: ws.ID, Title: "Checkout", SquadID: sq.ID, SquadSnapshot: sq, Status: "open", CreatedAt: domain.Now()}
	if err := s.PutMission(msn); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListMissions(ws.ID)
	if err != nil || len(list) != 1 || list[0].SquadSnapshot.Name != "Web" {
		t.Fatalf("%+v %v", list, err)
	}
	ev, err := s.AppendEvent("test", map[string]any{"n": 1})
	if err != nil || ev.Seq != 1 {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.AppendEvent("later", map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := s.EventsRecent(2)
	if err != nil || len(recent) != 2 || recent[1].Kind != "later" {
		t.Fatalf("%+v %v", recent, err)
	}
}
