package limits

import (
	"sync"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

// Collector observes account usage. It never starts a generation call
// and never changes which account a session uses.
type Collector interface {
	Snapshot(account domain.Account) domain.LimitSnapshot
}

type Plugin struct {
	mu     sync.Mutex
	inner  Collector
	cache  map[string]domain.LimitSnapshot
	all    []domain.LimitSnapshot
	allKey string
	allAt  time.Time
	allTTL time.Duration
}

func New(c Collector) *Plugin {
	if c == nil {
		c = Unavailable{}
	}
	return &Plugin{inner: c, cache: map[string]domain.LimitSnapshot{}, allTTL: 15 * time.Second}
}

func (p *Plugin) Snapshot(account domain.Account) domain.LimitSnapshot {
	now := time.Now().UTC()
	snap := p.inner.Snapshot(account)
	snap.AccountID = account.ID
	snap.Label = account.Label
	snap.LastAttempt = now
	if snap.Source == "" {
		snap.Source = domain.SourceUnknown
	}
	if snap.Percent != nil && snap.Source == domain.SourceUnknown {
		snap.Percent = nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if snap.Source == domain.SourceUnavailable || snap.Source == domain.SourceUnknown || snap.Source == domain.SourceUnsupported {
		if prev, ok := p.cache[account.ID]; ok && prev.ObservedAt != nil {
			prev.Source = domain.SourceStale
			prev.LastAttempt = now
			prev.Origin = snap.Origin
			p.cache[account.ID] = prev
			return prev
		}
	} else if snap.ObservedAt != nil {
		p.cache[account.ID] = snap
		return snap
	}
	p.cache[account.ID] = snap
	return snap
}

func (p *Plugin) All(accounts []domain.Account) []domain.LimitSnapshot {
	key := ""
	for _, a := range accounts {
		key += a.ID + ","
	}
	p.mu.Lock()
	ttl := p.allTTL
	if ttl == 0 {
		ttl = 15 * time.Second
	}
	if p.allKey == key && time.Since(p.allAt) < ttl && p.all != nil {
		out := append([]domain.LimitSnapshot(nil), p.all...)
		p.mu.Unlock()
		return out
	}
	p.mu.Unlock()
	out := make([]domain.LimitSnapshot, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, p.Snapshot(accountOr(a)))
	}
	p.mu.Lock()
	p.all, p.allKey, p.allAt = append([]domain.LimitSnapshot(nil), out...), key, time.Now()
	p.mu.Unlock()
	return out
}

func accountOr(a domain.Account) domain.Account { return a }

// Unavailable is the default collector: no invented percentages.
type Unavailable struct{}

func (Unavailable) Snapshot(account domain.Account) domain.LimitSnapshot {
	origin := "not_qualified"
	src := domain.SourceUnavailable
	if account.Adapter == "gemini" {
		src = domain.SourceUnsupported
	}
	return domain.LimitSnapshot{
		Window: "unknown",
		Source: src,
		Origin: origin,
	}
}
