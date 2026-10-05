package domain

import (
	"fmt"
	"strings"
)

// GateMark maps loop.PanelModel gate columns to a compact glyph.
func GateMark(state string) string {
	switch state {
	case "pass":
		return "✓"
	case "fail":
		return "✗"
	default:
		return "—"
	}
}

// FirstGates returns G0–G3 for the first task row in the watch DTO.
func (v WatchView) FirstGates() [4]string {
	out := [4]string{"—", "—", "—", "—"}
	for _, wave := range v.Waves {
		for _, row := range asMaps(wave["rows"]) {
			g := asStrings(row["gates"])
			for i := 0; i < 4 && i < len(g); i++ {
				out[i] = GateMark(g[i])
			}
			return out
		}
	}
	return out
}

func (v WatchView) FormatGates() string {
	g := v.FirstGates()
	return fmt.Sprintf("G0 %s  G1 %s  G2 %s  G3 %s", g[0], g[1], g[2], g[3])
}

func (v WatchView) AttentionLine() string {
	if v.Attention == nil {
		return ""
	}
	kind, _ := v.Attention["kind"].(string)
	text, _ := v.Attention["text"].(string)
	if kind == "none" {
		kind = ""
	}
	if kind == "" && text == "" {
		return ""
	}
	if text == "" {
		return kind
	}
	if kind == "" {
		return text
	}
	return kind + " · " + text
}

// ProcessLife labels catalog recovery after a daemon restart (I5).
func ProcessLife(process string) string {
	switch process {
	case "running":
		return "live"
	case "unknown":
		return "catalog only — process not live"
	case "exited":
		return "exited"
	default:
		return process
	}
}

type TaskRow struct {
	Task, Title, State string
	Gates              [4]string
}

type Criterion struct {
	Index int
	Title string
	State string
}

type Evidence struct {
	Kind, Text, Origin string
}

func (v WatchView) TaskRows() []TaskRow {
	var out []TaskRow
	for _, wave := range v.Waves {
		for _, row := range asMaps(wave["rows"]) {
			g := asStrings(row["gates"])
			tr := TaskRow{
				Task:  asString(row["task"]),
				Title: asString(row["title"]),
				State: asString(row["state"]),
			}
			for i := 0; i < 4 && i < len(g); i++ {
				tr.Gates[i] = GateMark(g[i])
			}
			if tr.Gates[0] == "" {
				tr.Gates = [4]string{"—", "—", "—", "—"}
			}
			out = append(out, tr)
		}
	}
	return out
}

func (v WatchView) Criteria() []Criterion {
	total := asInt(mapGet(v.Progress, "criterion_total"))
	if total == 0 {
		total = asInt(mapGet(v.Detail, "criterion_total"))
	}
	cur := asInt(mapGet(v.Progress, "criterion"))
	if cur == 0 {
		cur = asInt(mapGet(v.Detail, "criterion"))
	}
	title := asString(mapGet(v.Progress, "criterion_title"))
	if title == "" {
		title = asString(mapGet(v.Detail, "criterion_title"))
	}
	state := asString(mapGet(v.Detail, "criterion_state"))
	if total <= 0 && cur <= 0 && title == "" {
		return nil
	}
	if total <= 0 {
		total = 1
		if cur > total {
			total = cur
		}
	}
	out := make([]Criterion, 0, total)
	for i := 1; i <= total; i++ {
		st := "pending"
		if cur > 0 && i < cur {
			st = "pass"
		}
		if i == cur {
			switch strings.ToUpper(state) {
			case "DONE":
				st = "pass"
			case "FAIL", "FAILED":
				st = "fail"
			default:
				if state != "" {
					st = "pending"
				}
			}
		}
		label := fmt.Sprintf("criterion %d", i)
		if i == cur && title != "" {
			label = title
		}
		out = append(out, Criterion{Index: i, Title: label, State: st})
	}
	return out
}

func (v WatchView) Evidence() []Evidence {
	var out []Evidence
	if s := asString(mapGet(v.Detail, "last")); s != "" {
		out = append(out, Evidence{Kind: "journal", Text: s, Origin: "watch"})
	}
	if s := asString(mapGet(v.Detail, "log_path")); s != "" {
		out = append(out, Evidence{Kind: "log", Text: s, Origin: "journal"})
	}
	if s := asString(mapGet(v.Detail, "worktree")); s != "" {
		out = append(out, Evidence{Kind: "tree", Text: s, Origin: "journal"})
	}
	if s := asString(mapGet(v.Health, "last_error")); s != "" {
		out = append(out, Evidence{Kind: "error", Text: s, Origin: "health"})
	}
	for _, line := range v.LogLines {
		if line != "" {
			out = append(out, Evidence{Kind: "log", Text: line, Origin: "journal"})
		}
	}
	return out
}

func FormatLimit(snap LimitSnapshot) string {
	src := string(snap.Source)
	if src == "" {
		src = string(SourceUnknown)
	}
	age := "—"
	when := snap.LastAttempt
	if snap.Percent != nil && snap.ObservedAt != nil {
		when = *snap.ObservedAt
	}
	if !when.IsZero() {
		age = when.UTC().Format("15:04:05")
	}
	if snap.Percent == nil {
		origin := snap.Origin
		if origin == "" {
			origin = "unknown"
		}
		return fmt.Sprintf("unknown · %s · %s · %s", src, origin, age)
	}
	return fmt.Sprintf("%.0f%% · %s · %s · %s", *snap.Percent, src, snap.Origin, age)
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	default:
		return 0
	}
}

func mapGet(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

func asMaps(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
