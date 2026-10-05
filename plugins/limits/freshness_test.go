package limits

import (
	"os"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestImportFreshnessBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, adapter := range []string{"codex", "claude"} {
		for _, tc := range []struct {
			name   string
			age    time.Duration
			source domain.SourceState
		}{
			{"future nanosecond", -time.Nanosecond, domain.SourceUnknown},
			{"exact now", 0, domain.SourceCurrent},
			{"before cutoff", 5*time.Minute - time.Nanosecond, domain.SourceCurrent},
			{"exact cutoff", 5 * time.Minute, domain.SourceStale},
			{"after cutoff", 5*time.Minute + time.Nanosecond, domain.SourceStale},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "boundary", Adapter: adapter}
				observed := now.Add(-tc.age)
				putImport(t, home, a, importJSON(t, observed, 41))
				s := (Disk{Home: home}).snapshotAt(a, now)
				if tc.source == domain.SourceUnknown {
					requireEmpty(t, s, tc.source)
					return
				}
				if s.Source != tc.source || s.Percent == nil || *s.Percent != 41 || s.ObservedAt == nil || !s.ObservedAt.Equal(observed) {
					t.Fatalf("boundary: want %s at %s, got %+v", tc.source, observed, s)
				}
			})
		}
	}
}

// timedDisk uses the real importer and temporary files, with a fixed read time.
type timedDisk struct {
	disk  Disk
	now   time.Time
	reads int
}

func (d *timedDisk) Snapshot(a domain.Account) domain.LimitSnapshot {
	d.reads++
	return d.disk.snapshotAt(a, d.now)
}

func TestAllCacheAgesWithoutRefreshingMeasurementOrAttempt(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			home := t.TempDir()
			a := domain.Account{ID: "aging", Adapter: adapter}
			observed := now.Add(-5*time.Minute + time.Second)
			putImport(t, home, a, importJSON(t, observed, 41))
			d := &timedDisk{disk: Disk{Home: home}, now: now}
			p := New(d)
			first := p.allAtTime([]domain.Account{a}, now)[0]
			if first.Source != domain.SourceCurrent {
				t.Fatalf("initial: %+v", first)
			}
			for _, elapsed := range []time.Duration{time.Second, 14 * time.Second} {
				d.now = now.Add(elapsed)
				s := p.allAtTime([]domain.Account{a}, d.now)[0]
				if s.Source != domain.SourceStale || s.Percent == nil || *s.Percent != 41 || s.ObservedAt == nil || !s.ObservedAt.Equal(observed) || !s.LastAttempt.Equal(first.LastAttempt) || s.Origin != "imported:"+adapter {
					t.Fatalf("aged cache must retain measurement and actual attempt: %+v", s)
				}
				if d.reads != 1 {
					t.Fatalf("cache unexpectedly recollected %d times", d.reads)
				}
			}
			d.now = now.Add(15 * time.Second)
			s := p.allAtTime([]domain.Account{a}, d.now)[0]
			if d.reads != 2 || s.Source != domain.SourceStale || s.ObservedAt == nil || !s.ObservedAt.Equal(observed) {
				t.Fatalf("TTL expiry reads=%d snapshot=%+v", d.reads, s)
			}
		})
	}
}

func TestSnapshotInvalidatesAllCache(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			home := t.TempDir()
			a := domain.Account{ID: "invalidation", Adapter: adapter}
			observed := time.Now().UTC().Add(-time.Minute)
			path := putImport(t, home, a, importJSON(t, observed, 41))
			p := New(Disk{Home: home})
			if s := p.All([]domain.Account{a})[0]; s.Source != domain.SourceCurrent {
				t.Fatalf("initial: %+v", s)
			}
			fields := importFields(observed, 101)
			putImport(t, home, a, encodeImport(t, fields))
			requireEmpty(t, p.Snapshot(a), domain.SourceUnknown)
			requireEmpty(t, p.All([]domain.Account{a})[0], domain.SourceUnknown)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			requireEmpty(t, p.Snapshot(a), domain.SourceUnavailable)
			requireEmpty(t, p.All([]domain.Account{a})[0], domain.SourceUnavailable)
		})
	}
}

func TestImportOriginAlwaysIdentifiesImport(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			home := t.TempDir()
			a := domain.Account{ID: "origin", Adapter: adapter}
			fields := importFields(now.Add(-time.Minute), 41)
			fields["origin"] = "external exporter"
			putImport(t, home, a, encodeImport(t, fields))
			s := (Disk{Home: home}).snapshotAt(a, now)
			if s.Origin != "imported:"+adapter+":external exporter" {
				t.Fatalf("provenance: %+v", s)
			}
		})
	}
}
