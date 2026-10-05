package limits

import (
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

// Collector observes account usage. It never starts a generation call
// and never changes which account a session uses.
// Snapshot may be called concurrently.
type Collector interface {
	Snapshot(account domain.Account) domain.LimitSnapshot
}

type accountKey struct {
	id      string
	adapter string
}

type allAccountKey struct {
	accountKey
	label string
}

type Plugin struct {
	mu       sync.Mutex
	inner    Collector
	cache    map[accountKey]domain.LimitSnapshot
	all      []domain.LimitSnapshot
	allKey   []allAccountKey
	allAt    time.Time
	allTTL   time.Duration
	latest   map[accountKey]uint64
	attempt  uint64
	revision uint64
}

func New(c Collector) *Plugin {
	if c == nil {
		c = Unavailable{}
	}
	return &Plugin{inner: c, cache: map[accountKey]domain.LimitSnapshot{}, latest: map[accountKey]uint64{}, allTTL: 15 * time.Second}
}

func (p *Plugin) Snapshot(account domain.Account) domain.LimitSnapshot {
	snap, _ := p.snapshot(account, time.Now().UTC())
	return snap
}

func (p *Plugin) snapshot(account domain.Account, now time.Time) (domain.LimitSnapshot, uint64) {
	key := accountKey{id: account.ID, adapter: account.Adapter}
	p.mu.Lock()
	p.attempt++
	attempt := p.attempt
	p.latest[key] = attempt
	p.mu.Unlock()

	snap := p.inner.Snapshot(account)
	snap.AccountID = account.ID
	snap.Label = account.Label
	snap.LastAttempt = now
	if snap.Source == "" {
		snap.Source = domain.SourceUnknown
	}
	if snap.Source == domain.SourceUnknown || snap.Source == domain.SourceUnsupported {
		snap.Percent, snap.ObservedAt, snap.ResetAt = nil, nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.all = nil
	p.revision++
	// A slow earlier attempt may return to its caller, but must not restore
	// data that a newer attempt for this account has already invalidated.
	if p.latest[key] != attempt {
		return snap, p.revision
	}
	if snap.Source == domain.SourceUnavailable {
		if prev, ok := p.cache[key]; ok && prev.ObservedAt != nil && prev.Percent != nil {
			prev.Source = domain.SourceStale
			prev.LastAttempt = now
			prev.Label = account.Label
			p.cache[key] = prev
			return prev, p.revision
		}
	}
	// Invalid observations revoke last-good data; a later missing file must not
	// resurrect a measurement rejected by the importer.
	p.cache[key] = snap
	return snap, p.revision
}

func (p *Plugin) All(accounts []domain.Account) []domain.LimitSnapshot {
	return p.allAtTime(accounts, time.Now().UTC())
}

func (p *Plugin) allAtTime(accounts []domain.Account, now time.Time) []domain.LimitSnapshot {
	key := make([]allAccountKey, len(accounts))
	for i, a := range accounts {
		key[i] = allAccountKey{accountKey: accountKey{id: a.ID, adapter: a.Adapter}, label: a.Label}
	}
	p.mu.Lock()
	ttl := p.allTTL
	if ttl == 0 {
		ttl = 15 * time.Second
	}
	if sameAccounts(p.allKey, key) && now.Sub(p.allAt) < ttl && p.all != nil {
		for i := range p.all {
			snap := &p.all[i]
			if snap.Source == domain.SourceCurrent && strings.HasPrefix(snap.Origin, "imported:") && snap.ObservedAt != nil && now.Sub(*snap.ObservedAt) >= importedFreshness {
				snap.Source = domain.SourceStale
			}
		}
		out := append([]domain.LimitSnapshot(nil), p.all...)
		p.mu.Unlock()
		return out
	}
	revision := p.revision
	p.mu.Unlock()
	cacheable := true
	out := make([]domain.LimitSnapshot, 0, len(accounts))
	for _, a := range accounts {
		snap, collected := p.snapshot(a, now)
		out = append(out, snap)
		cacheable = cacheable && collected == revision+1
		revision = collected
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Do not publish a batch over an interleaved Snapshot or another batch.
	if cacheable && p.revision == revision {
		p.all, p.allKey, p.allAt = append([]domain.LimitSnapshot(nil), out...), key, now
	}
	return out
}

func sameAccounts(a, b []allAccountKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Unavailable is the default collector: no invented percentages.
type Unavailable struct{}

func (Unavailable) Snapshot(account domain.Account) domain.LimitSnapshot {
	src := domain.SourceUnavailable
	if account.Adapter != "codex" && account.Adapter != "claude" {
		src = domain.SourceUnsupported
	}
	return domain.LimitSnapshot{Window: "unknown", Source: src, Origin: "not_qualified"}
}
