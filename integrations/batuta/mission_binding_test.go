package batuta

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func TestMissionBindingJournalObservation(t *testing.T) {
	root := t.TempDir()
	if _, err := readJournal(root, "missing"); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("missing %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".batuta")); !os.IsNotExist(err) {
		t.Fatalf("read created directory %v", err)
	}
	js, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := js.Append("alpha", journal.Record{Kind: loop.KindOpened, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Detail: json.RawMessage(`{"slug":"alpha"}`), Graph: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(js.Path("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := Project(root, "")
	if err != nil || view.Available || view.Delivery != "" {
		t.Fatalf("implicit selection: %+v %v", view, err)
	}
	read, err := readJournal(root, "alpha")
	if err != nil || len(read) != 1 || read[0].Digest != rec.Digest {
		t.Fatalf("chain read %v %v", read, err)
	}
	after, err := os.ReadFile(js.Path("alpha"))
	if err != nil || string(after) != string(before) {
		t.Fatal("reader altered chain")
	}
	for _, id := range []string{"../alpha", "/alpha", "alpha.jsonl", "", string(make([]byte, 129))} {
		if _, err := readJournal(root, id); !errors.Is(err, domain.ErrInvalidReference) {
			t.Fatalf("invalid id %q: %v", id, err)
		}
	}
}

func TestMissionBindingRejectsOversizeAndNonregularArtifacts(t *testing.T) {
	for _, kind := range []string{"plan", "journal", "directory", "parent symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, journal.Dir), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".batuta/plans/alpha.md")
			size := int64((1 << 20) + 1)
			if kind == "journal" {
				path = filepath.Join(root, journal.Dir, "alpha.jsonl")
				size = (256 << 20) + 1
			}
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if kind == "parent symlink" {
				foreign := t.TempDir()
				if err := os.Remove(filepath.Join(root, journal.Dir)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, filepath.Join(root, journal.Dir)); err != nil {
					t.Fatal(err)
				}
			} else {
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(size); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if kind == "journal" || kind == "parent symlink" {
				_, err = readJournal(root, "alpha")
			} else {
				_, err = ExactPlan(root, ".batuta/plans/alpha.md")
			}
			if !errors.Is(err, ErrArtifactInvalid) {
				t.Fatalf("unsafe artifact: %v", err)
			}
		})
	}
}

func TestMissionBindingExactLegacyAndArchivedPlans(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta/plans/done"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("# Plan — alpha\n**Status:** approved\n- [ ] 1. Work — data/high\n  Accept: verified\n")
	for _, path := range []string{".batuta/plan-alpha.md", ".batuta/plans/done/alpha.md"} {
		if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
			t.Fatal(err)
		}
		plan, err := ExactPlan(root, path)
		if err != nil || plan.Path != path {
			t.Fatalf("exact plan: %+v %v", plan, err)
		}
		parsed, err := routing.ParsePlan("alpha", body)
		if err != nil || plan.Set.Digest != parsed.Set.Digest {
			t.Fatalf("core identity %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".batuta/plans/alpha.md"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExactPlan(root, ".batuta/plan-alpha.md"); !errors.Is(err, ErrArtifactInvalid) {
		t.Fatalf("ambiguous active plan %v", err)
	}
	if _, err := ExactPlan(root, ".batuta/plans/done/alpha.md"); err != nil {
		t.Fatalf("archive must retain exact selection %v", err)
	}
}
