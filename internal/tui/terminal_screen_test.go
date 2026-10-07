package tui

import (
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/charmbracelet/x/ansi"
)

func TestTerminalViewCellScreen(t *testing.T) {
	m := model{screen: scrRoom, termMode: true, width: 4, height: 20, sessID: "selected",
		sessions: []domain.Session{{ID: "selected", Cols: 4, Rows: 2}}}
	next, cmd := m.Update(ptyOutMsg("1234\r\nabcdX"))
	if cmd != nil {
		if msg := cmd(); msg != nil {
			next, _ = next.Update(msg)
		}
	}
	if got := ansi.Strip(next.View().Content); !strings.Contains(got, "\nabcd\nX   \n") {
		t.Fatalf("missing literal cell screen abcd / X___: %q", got)
	}
}
