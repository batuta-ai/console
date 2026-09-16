package limits

import (
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

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
