package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

// Disk reads a limit snapshot from the account isolate directory when a CLI
// has left a JSON file. Missing files stay unavailable; percentages are never
// invented.
type Disk struct {
	Home string
}

type diskFile struct {
	Percent *float64 `json:"percent"`
	Window  string   `json:"window"`
	Origin  string   `json:"origin"`
}

func (d Disk) Snapshot(account domain.Account) domain.LimitSnapshot {
	fallback := Unavailable{}.Snapshot(account)
	if d.Home == "" || account.ID == "" {
		return fallback
	}
	ad, err := adapters.Get(account.Adapter)
	if err != nil {
		return fallback
	}
	root := ad.IsolateDir(d.Home, account.ID)
	var path string
	switch account.Adapter {
	case "codex":
		path = filepath.Join(root, ".codex", "rate_limit.json")
	case "claude":
		path = filepath.Join(root, ".claude", "rate_limit.json")
	default:
		return fallback
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	var file diskFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Percent == nil {
		return fallback
	}
	now := time.Now().UTC()
	origin := file.Origin
	if origin == "" {
		origin = account.Adapter
	}
	window := file.Window
	if window == "" {
		window = "unknown"
	}
	return domain.LimitSnapshot{
		Window:     window,
		Percent:    file.Percent,
		ObservedAt: &now,
		Source:     domain.SourceCurrent,
		Origin:     origin,
	}
}
