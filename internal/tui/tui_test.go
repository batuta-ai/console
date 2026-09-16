package tui

import (
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestASCIIModeNO_COLOR(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if !asciiMode() {
		t.Fatal("NO_COLOR must force ASCII")
	}
	m := model{screen: scrRoom, width: 80, watch: domain.WatchView{Available: true, Delivery: "demo"}}
	out := m.render()
	if strings.Contains(out, "─") || strings.Contains(out, "✓") || strings.Contains(out, "—") {
		t.Fatalf("unicode leaked under NO_COLOR:\n%s", out)
	}
}

func TestRenderReviewCriteria(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{
		screen: scrReview, width: 80,
		watch: domain.WatchView{
			Available: true, Delivery: "demo",
			Progress: map[string]any{"criterion": 1.0, "criterion_total": 2.0, "criterion_title": "Tick the phase"},
			Detail:   map[string]any{"criterion_state": "DONE", "last": "task_progress 1 DONE"},
		},
	}
	out := m.render()
	if !strings.Contains(out, "CRITERIA") || !strings.Contains(out, "EVIDENCE") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "Tick the phase") || !strings.Contains(out, "task_progress") {
		t.Fatal(out)
	}
}

func TestRenderRoomTaskColumns(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := model{
		screen: scrRoom, width: 80,
		watch: domain.WatchView{Waves: []map[string]any{{
			"rows": []map[string]any{{"task": "task_1", "title": "Checkout", "state": "running", "gates": []string{"pass", "pending", "pending", "pending"}}},
		}}},
	}
	out := m.render()
	if !strings.Contains(out, "TASK") || !strings.Contains(out, "Checkout") {
		t.Fatal(out)
	}
}
