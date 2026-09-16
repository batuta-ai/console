package batuta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestProjectMissingJournal(t *testing.T) {
	view, err := Project(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if view.Available {
		t.Fatalf("expected unavailable: %+v", view)
	}
}

func TestProjectPanelModel(t *testing.T) {
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	graph, _ := json.Marshal(map[string]any{"tasks": []any{}, "waves": []any{}})
	rec := journal.Record{Kind: loop.KindOpened, At: time.Now().UTC(), Graph: graph, Detail: []byte(`{"slug":"demo","workspace":"` + root + `"}`)}
	if _, err := store.Append("demo-delivery", rec); err != nil {
		t.Fatal(err)
	}
	view, err := Project(root, "demo-delivery")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Available || view.Delivery != "demo-delivery" {
		t.Fatalf("%+v", view)
	}
	if view.Header["delivery"] != loop.PanelModel(mustRead(t, store, "demo-delivery"), time.Now().UTC(), "").Header.Delivery && view.Header["delivery"] == nil {
		t.Fatalf("header %+v", view.Header)
	}
}

func mustRead(t *testing.T, store *journal.Store, id string) []journal.Record {
	t.Helper()
	recs, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestPrepareDoesNotInventSkills(t *testing.T) {
	t.Setenv("BATUTA_SKILLS", filepath.Join(t.TempDir(), "missing"))
	p := Prepare(t.TempDir(), true)
	if p.Enabled {
		t.Fatalf("must stay off without skills: %+v", p)
	}
}

func TestPrepareOff(t *testing.T) {
	p := Prepare("/tmp/ws", false)
	if p.Enabled {
		t.Fatal("expected off")
	}
}

func TestPrepareUsesSkillsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skills-lock.json"), []byte(`{"version":"v0.7.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATUTA_SKILLS", dir)
	p := Prepare("/tmp/ws", true)
	if !p.Enabled || p.SkillsVersion != "v0.7.0" {
		t.Fatalf("%+v", p)
	}
}
