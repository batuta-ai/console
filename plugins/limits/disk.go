package limits

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

const importedFreshness = 5 * time.Minute
const maxImportBytes = 16 * 1024

// Disk reads externally supplied quota observations from isolated account files.
// These files are not a qualified native provider quota API.
type Disk struct {
	Home string
}

type diskFile struct {
	Percent    *float64   `json:"percent"`
	Window     string     `json:"window"`
	Scope      string     `json:"scope"`
	ObservedAt *time.Time `json:"observed_at"`
	ResetAt    *time.Time `json:"reset_at"`
	Origin     string     `json:"origin"`
}

func (d Disk) Snapshot(account domain.Account) domain.LimitSnapshot {
	return d.snapshotAt(account, time.Now().UTC())
}

func (d Disk) snapshotAt(account domain.Account, now time.Time) domain.LimitSnapshot {
	fallback := Unavailable{}.Snapshot(account)
	if fallback.Source == domain.SourceUnsupported || d.Home == "" || account.ID == "" {
		return fallback
	}
	ad, err := adapters.Get(account.Adapter)
	if err != nil {
		return fallback
	}
	path := filepath.Join(ad.IsolateDir(d.Home, account.ID), "."+account.Adapter, "rate_limit.json")
	info, err := os.Stat(path)
	if err != nil {
		return fallback
	}
	invalid := fallback
	invalid.Source = domain.SourceUnknown
	invalid.Origin = "imported:" + account.Adapter
	if !info.Mode().IsRegular() {
		return invalid
	}
	// Nonblocking open also covers a regular path replaced by a FIFO after Stat.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fallback
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return fallback
	}
	if !info.Mode().IsRegular() || info.Size() > maxImportBytes {
		return invalid
	}
	// Bound the read too: the file may grow after the descriptor's Stat.
	raw, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
	if err != nil {
		return fallback
	}
	if len(raw) > maxImportBytes {
		return invalid
	}

	fallback = invalid
	var file diskFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return fallback
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fallback
	}
	if file.Percent == nil || *file.Percent < 0 || *file.Percent > 100 || strings.TrimSpace(file.Window) == "" || file.Scope != "account" {
		return fallback
	}
	if file.ObservedAt == nil || file.ObservedAt.IsZero() || file.ObservedAt.After(now) {
		return fallback
	}
	if file.ResetAt != nil && (file.ResetAt.IsZero() || file.ResetAt.Before(*file.ObservedAt)) {
		return fallback
	}
	source := domain.SourceCurrent
	if now.Sub(*file.ObservedAt) >= importedFreshness {
		source = domain.SourceStale
	}
	origin := fallback.Origin
	if supplied := strings.TrimSpace(file.Origin); supplied != "" {
		origin += ":" + supplied
	}
	return domain.LimitSnapshot{
		Window:     strings.TrimSpace(file.Window),
		Scope:      file.Scope,
		Percent:    file.Percent,
		ObservedAt: file.ObservedAt,
		ResetAt:    file.ResetAt,
		Source:     source,
		Origin:     origin,
	}
}
