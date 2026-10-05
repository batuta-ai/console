package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

func importFields(observed time.Time, percent float64) map[string]any {
	return map[string]any{"percent": percent, "window": "5h", "scope": "account", "observed_at": observed.Format(time.RFC3339Nano)}
}

func encodeImport(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func importJSON(t *testing.T, observed time.Time, percent float64) []byte {
	t.Helper()
	return encodeImport(t, importFields(observed, percent))
}

func putImport(t *testing.T, home string, a domain.Account, raw []byte) string {
	t.Helper()
	ad, err := adapters.Get(a.Adapter)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ad.IsolateDir(home, a.ID), "."+a.Adapter)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rate_limit.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireEmpty(t *testing.T, s domain.LimitSnapshot, source domain.SourceState) {
	t.Helper()
	if s.Source != source || s.Percent != nil || s.ObservedAt != nil || s.ResetAt != nil {
		t.Fatalf("want %s without measurement, got %+v", source, s)
	}
}

func TestImportedMeasurementTimeAndFreshness(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, age := range []time.Duration{time.Minute, 24 * time.Hour} {
			t.Run(adapter+"/"+age.String(), func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "measurement", Label: "Original", Adapter: adapter}
				observed := time.Now().UTC().Add(-age).Truncate(time.Second)
				putImport(t, home, a, importJSON(t, observed, 41))
				p := New(Disk{Home: home})
				want := domain.SourceCurrent
				if age > 5*time.Minute {
					want = domain.SourceStale
				}
				for range 2 {
					s := p.Snapshot(a)
					if s.Source != want || s.Percent == nil || *s.Percent != 41 || s.ObservedAt == nil || !s.ObservedAt.Equal(observed) || s.Scope != "account" || s.Origin != "imported:"+adapter {
						t.Fatalf("want %s imported 41%% measured %s, got %+v", want, observed, s)
					}
					if !s.LastAttempt.After(observed) {
						t.Fatalf("attempt must not replace observation: %+v", s)
					}
				}
			})
		}
	}
}

func TestImportValidation(t *testing.T) {
	observed := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	cases := []struct {
		name, field string
		value       any
		absent      bool
		valid       bool
	}{
		{name: "observed absent", field: "observed_at", absent: true},
		{name: "observed null", field: "observed_at", value: nil},
		{name: "observed invalid", field: "observed_at", value: "yesterday"},
		{name: "observed zero", field: "observed_at", value: "0001-01-01T00:00:00Z"},
		{name: "observed future", field: "observed_at", value: observed.Add(24 * time.Hour).Format(time.RFC3339)},
		{name: "window absent", field: "window", absent: true},
		{name: "window blank", field: "window", value: ""},
		{name: "window whitespace", field: "window", value: " \t\n"},
		{name: "percent absent", field: "percent", absent: true},
		{name: "percent null", field: "percent", value: nil},
		{name: "percent string", field: "percent", value: "41"},
		{name: "percent negative", field: "percent", value: -0.01},
		{name: "percent too large", field: "percent", value: 100.01},
		{name: "percent zero", field: "percent", value: 0, valid: true},
		{name: "percent hundred", field: "percent", value: 100, valid: true},
		{name: "scope absent", field: "scope", absent: true},
		{name: "scope wrong", field: "scope", value: "organization"},
		{name: "scope session", field: "scope", value: "session"},
		{name: "scope provider mismatch", field: "scope", value: "other-provider"},
		{name: "reset absent", field: "reset_at", absent: true, valid: true},
		{name: "reset null", field: "reset_at", value: nil, valid: true},
		{name: "reset valid", field: "reset_at", value: observed.Add(time.Hour).Format(time.RFC3339), valid: true},
		{name: "reset malformed", field: "reset_at", value: "tomorrow"},
		{name: "reset zero", field: "reset_at", value: "0001-01-01T00:00:00Z"},
		{name: "reset before observation", field: "reset_at", value: observed.Add(-time.Second).Format(time.RFC3339)},
		{name: "unknown field", field: "unexpected", value: true},
	}
	for _, adapter := range []string{"codex", "claude"} {
		for _, tc := range cases {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "validation", Adapter: adapter}
				fields := importFields(observed, 41)
				if tc.absent {
					delete(fields, tc.field)
				} else {
					fields[tc.field] = tc.value
				}
				putImport(t, home, a, encodeImport(t, fields))
				s := New(Disk{Home: home}).Snapshot(a)
				if !tc.valid {
					requireEmpty(t, s, domain.SourceUnknown)
					return
				}
				wantPercent := 41.0
				if tc.field == "percent" {
					wantPercent = float64(tc.value.(int))
				}
				if s.Source != domain.SourceCurrent || s.Percent == nil || *s.Percent != wantPercent || s.ObservedAt == nil || !s.ObservedAt.Equal(observed) {
					t.Fatalf("valid import lost measurement: %+v", s)
				}
				if tc.name == "reset valid" {
					if s.ResetAt == nil || !s.ResetAt.Equal(observed.Add(time.Hour)) {
						t.Fatalf("lost reset: %+v", s)
					}
				} else if s.ResetAt != nil {
					t.Fatalf("invented reset: %+v", s)
				}
			})
		}
		for _, raw := range []string{`{"percent":`, `null`, `[]`, string(importJSON(t, observed, 41)) + ` {}`, string(importJSON(t, observed, 41)) + ` garbage`} {
			t.Run(adapter+"/json/"+raw, func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "json", Adapter: adapter}
				putImport(t, home, a, []byte(raw))
				requireEmpty(t, New(Disk{Home: home}).Snapshot(a), domain.SourceUnknown)
			})
		}
	}
}

func TestInvalidImportClearsLastGood(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"Snapshot", "All"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "invalid", Adapter: adapter, SessionCap: 9}
				p := New(Disk{Home: home})
				collect := func() domain.LimitSnapshot {
					if mode == "Snapshot" {
						return p.Snapshot(a)
					}
					p.allAt = time.Time{} // A new collection, outside the documented All TTL.
					return p.All([]domain.Account{a})[0]
				}
				path := putImport(t, home, a, importJSON(t, time.Now().UTC().Add(-time.Minute), 41))
				if s := collect(); s.Source != domain.SourceCurrent || s.Percent == nil || *s.Percent != 41 {
					t.Fatalf("valid baseline: %+v", s)
				}
				putImport(t, home, a, []byte(`{"percent":null,"window":"5h","scope":"account"}`))
				requireEmpty(t, collect(), domain.SourceUnknown)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				requireEmpty(t, collect(), domain.SourceUnavailable)
			})
		}
	}
}

func TestCacheAccountIdentity(t *testing.T) {
	for _, from := range []string{"codex", "claude"} {
		for _, to := range []string{"", "cursor", "opencode", "gemini", "codex", "claude"} {
			if from == to {
				continue
			}
			for _, mode := range []string{"Snapshot", "All"} {
				t.Run(from+"-"+to+"/"+mode, func(t *testing.T) {
					home := t.TempDir()
					a := domain.Account{ID: "synthetic-a", Label: "Before", Adapter: from}
					putImport(t, home, a, importJSON(t, time.Now().UTC().Add(-time.Minute), 41))
					p := New(Disk{Home: home})
					collect := func(a domain.Account) domain.LimitSnapshot {
						if mode == "All" {
							return p.All([]domain.Account{a})[0]
						}
						return p.Snapshot(a)
					}
					if s := collect(a); s.Percent == nil || *s.Percent != 41 {
						t.Fatalf("initial measurement: %+v", s)
					}
					a.Adapter = to
					a.Label = "After"
					s := collect(a)
					want := domain.SourceUnsupported
					if to == "codex" || to == "claude" {
						want = domain.SourceUnavailable
					}
					requireEmpty(t, s, want)
					if s.AccountID != a.ID || s.Label != a.Label {
						t.Fatalf("identity: %+v", s)
					}
				})
			}
		}
	}
}

func TestCacheLabelsAndUnavailableProvenance(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"Snapshot", "All"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "label", Label: "Before", Adapter: adapter}
				observed := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
				path := putImport(t, home, a, importJSON(t, observed, 41))
				p := New(Disk{Home: home})
				collect := func() domain.LimitSnapshot {
					if mode == "All" {
						return p.All([]domain.Account{a})[0]
					}
					return p.Snapshot(a)
				}
				collect()
				a.Label = "After"
				s := collect()
				if s.Label != a.Label {
					t.Fatalf("old label: %+v", s)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				p.allAt = time.Time{}
				s = collect()
				if s.Source != domain.SourceStale || s.Percent == nil || *s.Percent != 41 || s.ObservedAt == nil || !s.ObservedAt.Equal(observed) || s.Origin != "imported:"+adapter || s.Label != a.Label {
					t.Fatalf("unavailable must retain honest last good: %+v", s)
				}
			})
		}
	}
}

func TestSessionCapacityIsNotQuota(t *testing.T) {
	for _, adapter := range []string{"codex", "claude", "", "cursor", "opencode", "gemini"} {
		t.Run(adapter, func(t *testing.T) {
			a := domain.Account{ID: "no-file", Adapter: adapter, SessionCap: 7}
			want := domain.SourceUnsupported
			if adapter == "codex" || adapter == "claude" {
				want = domain.SourceUnavailable
			}
			requireEmpty(t, New(Disk{Home: t.TempDir()}).All([]domain.Account{a})[0], want)
		})
	}
}

func TestImportedFormatLimit(t *testing.T) {
	observed := time.Date(2026, 10, 1, 12, 34, 56, 0, time.UTC)
	attempt := observed.Add(time.Hour)
	percent := 41.0
	for _, source := range []domain.SourceState{domain.SourceCurrent, domain.SourceStale} {
		s := domain.LimitSnapshot{Source: source, Percent: &percent, ObservedAt: &observed, LastAttempt: attempt, Origin: "imported:codex"}
		formatted := domain.FormatLimit(s)
		for _, part := range []string{"41%", string(source), "imported:codex", "12:34:56"} {
			if !strings.Contains(formatted, part) {
				t.Errorf("missing %q: %q", part, formatted)
			}
		}
		if strings.Contains(formatted, "13:34:56") {
			t.Errorf("observation replaced by attempt: %q", formatted)
		}
	}
	unknown := domain.FormatLimit(domain.LimitSnapshot{Source: domain.SourceUnknown, Origin: "imported:claude", LastAttempt: attempt})
	if unknown != "unknown · unknown · imported:claude · 13:34:56" {
		t.Fatalf("unknown branch: %q", unknown)
	}
	if strings.Contains(unknown, "0%") {
		t.Fatal("invented 0%")
	}
}
