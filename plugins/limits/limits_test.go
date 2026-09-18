package limits

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

func TestDiskReadsCodexFile(t *testing.T) {
	home := t.TempDir()
	a := domain.Account{ID: "acct_codex", Adapter: "codex"}
	ad, err := adapters.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ad.IsolateDir(home, a.ID), ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rate_limit.json"), []byte(`{"percent":41,"window":"5h","origin":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := New(Disk{Home: home}).Snapshot(a)
	if snap.Percent == nil || *snap.Percent != 41 || snap.Source != domain.SourceCurrent {
		t.Fatalf("%+v", snap)
	}
}

func TestDiskReadsClaudeFile(t *testing.T) {
	home := t.TempDir()
	a := domain.Account{ID: "acct_claude", Adapter: "claude"}
	ad, err := adapters.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ad.IsolateDir(home, a.ID), ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rate_limit.json"), []byte(`{"percent":18,"window":"5h"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := New(Disk{Home: home}).Snapshot(a)
	if snap.Percent == nil || *snap.Percent != 18 || snap.Source != domain.SourceCurrent {
		t.Fatalf("%+v", snap)
	}
}

func TestDiskMissingStaysUnavailable(t *testing.T) {
	snap := New(Disk{Home: t.TempDir()}).Snapshot(domain.Account{ID: "acct_x", Adapter: "codex"})
	if snap.Percent != nil || snap.Source != domain.SourceUnavailable {
		t.Fatalf("%+v", snap)
	}
}

func TestUnknownIsNotZero(t *testing.T) {
	p := New(Unavailable{})
	snap := p.Snapshot(domain.Account{ID: "a", Label: "Codex · Work", Adapter: "codex"})
	if snap.Percent != nil {
		t.Fatalf("unknown must not become 0%%: %+v", snap)
	}
	if snap.Source != domain.SourceUnavailable {
		t.Fatalf("source %s", snap.Source)
	}
}

type failThenOK struct{ n int }

func (f *failThenOK) Snapshot(account domain.Account) domain.LimitSnapshot {
	f.n++
	if f.n == 1 {
		pct := 62.0
		now := time.Now().UTC()
		return domain.LimitSnapshot{Window: "5h", Percent: &pct, ObservedAt: &now, Source: domain.SourceCurrent, Origin: "fixture"}
	}
	return domain.LimitSnapshot{Window: "5h", Source: domain.SourceUnavailable, Origin: "network"}
}

type countCol struct{ n int }

func (f *countCol) Snapshot(account domain.Account) domain.LimitSnapshot {
	f.n++
	return domain.LimitSnapshot{Source: domain.SourceUnavailable, Origin: "x"}
}

func TestAllDoesNotDoubleCollect(t *testing.T) {
	c := &countCol{}
	p := New(c)
	accts := []domain.Account{{ID: "a"}, {ID: "b"}}
	p.All(accts)
	p.All(accts)
	if c.n != 2 {
		t.Fatalf("inner called %d, want 2 (second All is cached)", c.n)
	}
}

func TestFailureKeepsLastGood(t *testing.T) {
	p := New(&failThenOK{})
	a := domain.Account{ID: "a", Label: "x", Adapter: "codex"}
	first := p.Snapshot(a)
	if first.Percent == nil || *first.Percent != 62 {
		t.Fatalf("%+v", first)
	}
	second := p.Snapshot(a)
	if second.Percent == nil || *second.Percent != 62 {
		t.Fatalf("should keep last good: %+v", second)
	}
	if second.Source != domain.SourceStale {
		t.Fatalf("source %s", second.Source)
	}
}
