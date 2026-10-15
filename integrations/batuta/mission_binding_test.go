package batuta

import (
	"context"
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

func TestJournalObservationRejectsUnpublishedSuffix(t *testing.T) {
	for _, suffix := range []string{"missing-newline", "truncated-record", "malformed-record"} {
		t.Run(suffix, func(t *testing.T) {
			root, b, a := approvedProjectionFixture(t)
			path := filepath.Join(root, journal.Dir, b.DeliveryID+".jsonl")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch suffix {
			case "missing-newline":
				body = body[:len(body)-1]
			case "truncated-record":
				body = append(body, []byte(`{"seq":2,"kind":"dispatch_intent"`)...)
			case "malformed-record":
				body = append(body, []byte("not-json\n")...)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			checks := map[string]func() error{
				"journal":  func() error { _, err := readJournal(root, b.DeliveryID); return err },
				"binding":  func() error { _, err := ValidateDelivery(root, b); return err },
				"approval": func() error { _, err := ValidateApprovedDelivery(root, b, a); return err },
				"mission":  func() error { _, err := ProjectMission(root, b); return err },
				"native":   func() error { _, err := ProjectApprovedMission(root, b, a); return err },
			}
			for name, check := range checks {
				if err := check(); !errors.Is(err, ErrArtifactInvalid) {
					t.Errorf("%s accepted %s: %v", name, suffix, err)
				}
			}
		})
	}
}

func TestBindingSnapshotPreservesStrictChainAndGraphValidation(t *testing.T) {
	for _, change := range []string{"duplicate-opening", "missing-opening", "null-graph", "invalid-graph", "changed-digest", "broken-chain"} {
		t.Run(change, func(t *testing.T) {
			root, b, a := approvedProjectionFixture(t)
			js, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "duplicate-opening":
				records, err := js.Read(b.DeliveryID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := js.Append(b.DeliveryID, records[0]); err != nil {
					t.Fatal(err)
				}
			case "missing-opening", "null-graph", "invalid-graph":
				b = rewriteProjectionOpening(t, root, b, func(record *journal.Record) {
					switch change {
					case "missing-opening":
						record.Kind = loop.KindStarted
					case "null-graph":
						record.Graph = json.RawMessage(`null`)
					case "invalid-graph":
						record.Graph = json.RawMessage(`{"tasks":"wrong-type"}`)
					}
				})
			case "changed-digest", "broken-chain":
				records, err := js.Read(b.DeliveryID)
				if err != nil {
					t.Fatal(err)
				}
				if change == "changed-digest" {
					records[0].Digest = "foreign"
				} else {
					records[0].Seq = 2
				}
				body, err := json.Marshal(records[0])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(js.Path(b.DeliveryID), append(body, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ValidateDelivery(root, b); !errors.Is(err, ErrArtifactInvalid) {
				t.Fatalf("validated %s: %v", change, err)
			}
			if view, err := ProjectMission(root, b); !errors.Is(err, ErrArtifactInvalid) || view.Available {
				t.Fatalf("projected %s: %+v, %v", change, view, err)
			}
			if view, err := ProjectApprovedMission(root, b, a); !errors.Is(err, ErrArtifactInvalid) || view.Available {
				t.Fatalf("native projected %s: %+v, %v", change, view, err)
			}
		})
	}
}

func TestJournalSnapshotCompatibilityAndCancellation(t *testing.T) {
	root, b, _ := approvedProjectionFixture(t)
	path := filepath.Join(root, journal.Dir, b.DeliveryID+".jsonl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := deliverySnapshot(context.Background(), root, b)
	if err != nil || !snapshot.Complete() || len(snapshot.Records()) != 1 {
		t.Fatalf("blank-line compatibility: %+v, %v", snapshot, err)
	}
	records := snapshot.Records()
	records[0].Detail[0], records[0].Graph[0], records[0].Digest = '!', '!', "foreign"
	if fresh := snapshot.Records(); fresh[0].Detail[0] != '{' || fresh[0].Graph[0] != '{' || fresh[0].Digest != b.OpenedDigest {
		t.Fatal("record copy mutated authoritative capture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if captured, err := deliverySnapshot(ctx, root, b); !errors.Is(err, context.Canceled) || captured.Complete() || len(captured.Records()) != 0 {
		t.Fatalf("canceled capture: %+v, %v", captured, err)
	}
	if _, err := ValidateDelivery(root, b); err != nil {
		t.Fatalf("cancellation retained ownership: %v", err)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readJournal(root, b.DeliveryID); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("empty journal: %v", err)
	}
}
