package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

type atomicCatalog interface {
	PutWorkspaceWithEvent(domain.Workspace, string, map[string]any) error
	PutSquadWithEvent(domain.Squad, string, map[string]any) error
	PutMissionWithEvent(domain.Mission, string, map[string]any) error
	PutAccountWithEvent(domain.Account, string, map[string]any) error
	PutSessionWithEvent(domain.Session, string, map[string]any) error
}

func TestAtomicCatalogMutations(t *testing.T) {
	for _, table := range []string{"workspaces", "squads", "missions", "accounts", "sessions"} {
		for _, update := range []bool{false, true} {
			for _, failure := range []string{"none", "eventSQL", "eventJSON", "writeSQL"} {
				t.Run(table+"/"+map[bool]string{true: "update", false: "create"}[update]+"/"+failure, func(t *testing.T) {
					s, err := Open(filepath.Join(t.TempDir(), "db"))
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					atomic, ok := any(s).(atomicCatalog)
					if !ok {
						t.Fatal("store has no explicit atomic catalog/event contract")
					}
					var bare func(string) error
					var put func(string, map[string]any) error
					switch table {
					case "workspaces":
						bare = func(v string) error { return s.PutWorkspace(domain.Workspace{ID: "id", Name: v, Root: "root"}) }
						put = func(v string, p map[string]any) error {
							return atomic.PutWorkspaceWithEvent(domain.Workspace{ID: "id", Name: v, Root: "root"}, "changed", p)
						}
					case "squads":
						bare = func(v string) error { return s.PutSquad(domain.Squad{ID: "id", Name: v}) }
						put = func(v string, p map[string]any) error {
							return atomic.PutSquadWithEvent(domain.Squad{ID: "id", Name: v}, "changed", p)
						}
					case "missions":
						bare = func(v string) error { return s.PutMission(domain.Mission{ID: "id", Title: v}) }
						put = func(v string, p map[string]any) error {
							return atomic.PutMissionWithEvent(domain.Mission{ID: "id", Title: v}, "changed", p)
						}
					case "accounts":
						bare = func(v string) error { return s.PutAccount(domain.Account{ID: "id", Label: v}) }
						put = func(v string, p map[string]any) error {
							return atomic.PutAccountWithEvent(domain.Account{ID: "id", Label: v}, "changed", p)
						}
					case "sessions":
						bare = func(v string) error { return s.PutSession(domain.Session{ID: "id", Owner: v}) }
						put = func(v string, p map[string]any) error {
							return atomic.PutSessionWithEvent(domain.Session{ID: "id", Owner: v}, "changed", p)
						}
					}
					if update {
						if err := bare("old"); err != nil {
							t.Fatal(err)
						}
					}
					before := rawRows(t, s.db, "SELECT * FROM "+table)
					payload := map[string]any{"id": "id", "value": "new"}
					switch failure {
					case "eventSQL":
						execSQL(t, s.db, `CREATE TRIGGER fail BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'event failure'); END`)
					case "eventJSON":
						payload["invalid"] = make(chan int)
					case "writeSQL":
						execSQL(t, s.db, "CREATE TRIGGER fail BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'write failure'); END")
					}
					err = put("new", payload)
					if failure != "none" {
						if err == nil {
							t.Fatal("failure not returned")
						}
						if !reflect.DeepEqual(before, rawRows(t, s.db, "SELECT * FROM "+table)) {
							t.Fatal("mutation not rolled back")
						}
						events, e := s.EventsSince(0, 100)
						if e != nil || len(events) != 0 {
							t.Fatalf("event escaped rollback: %v %v", events, e)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					after := rawRows(t, s.db, "SELECT * FROM "+table)
					if len(after) != 1 || reflect.DeepEqual(before, after) {
						t.Fatalf("mutation missing: %v", after)
					}
					events, err := s.EventsSince(0, 100)
					if err != nil || len(events) != 1 || events[0].Kind != "changed" || !reflect.DeepEqual(events[0].Payload, payload) {
						t.Fatalf("events %v %v", events, err)
					}
				})
			}
		}
	}
}

// SQLite supplies LastInsertId; a driver result error must still reach callers.
type badIDResult struct{}

func (badIDResult) LastInsertId() (int64, error) { return 0, sql.ErrConnDone }
func (badIDResult) RowsAffected() (int64, error) { return 1, nil }

type badIDExecutor struct{}

func (badIDExecutor) Exec(string, ...any) (sql.Result, error) { return badIDResult{}, nil }

func TestEventRetentionAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if _, err := s.AppendEvent("retained", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recent, err := s.EventsRecent(0)
	if err != nil || len(recent) != 100 {
		t.Fatalf("recent: %d %v", len(recent), err)
	}
	bounded, err := s.EventsSince(0, 0)
	if err != nil || len(bounded) != 200 {
		t.Fatalf("bounded: %d %v", len(bounded), err)
	}
	all, err := s.EventsSince(0, 1000)
	if err != nil || len(all) != 250 || all[0].Seq != 1 || all[249].Seq != 250 {
		t.Fatalf("retention: %d %v", len(all), err)
	}
	if _, err := s.AppendEvent("bad", map[string]any{"x": make(chan int)}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal(err)
	}
}

func TestAppendEventLastInsertIDFailure(t *testing.T) {
	_, err := (catalogWriter{db: badIDExecutor{}}).AppendEvent("test", nil)
	if err != sql.ErrConnDone {
		t.Fatalf("LastInsertId error lost: %v", err)
	}
}
