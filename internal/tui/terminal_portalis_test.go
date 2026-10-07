package tui

import (
	"testing"

	portalis "github.com/batuta-ai/console/internal/terminalengine"
	"github.com/charmbracelet/x/ansi"
)

// This independent public-API reproduction isolates the model regression from
// Console's framing, clipping, message handling and transport.
func TestTerminalPortalisAlternateResizeCursor(t *testing.T) {
	for _, mode := range []string{"", "1047", "1049"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			s := portalis.NewScreen(4, 4)
			s.SetScrollbackLimit(400)
			s.SetCursorBlinkVisible(true)
			p := portalis.NewParser(s)
			p.Feed([]byte("1111\r\n2222\r\n3333\r\n444"))
			if mode != "" {
				p.Feed([]byte("\x1b[?" + mode + "hALT"))
			}
			for _, rows := range []int{2, 4} {
				if err := p.ResizeChecked(rows, 4); err != nil {
					t.Fatal(err)
				}
				s.SetCursorBlinkVisible(true)
			}
			if mode != "" {
				p.Feed([]byte("\x1b[?" + mode + "l"))
			}
			if got := ansi.Strip(s.Render()); got != "3333\n444 \n    \n    " {
				t.Errorf("main grid=%q", got)
			}
			if cursor := s.CursorState(); cursor.Row != 1 || cursor.Col != 3 {
				t.Errorf("restored cursor=(%d,%d), want=(1,3)", cursor.Row, cursor.Col)
			}
			p.Feed([]byte("X"))
			if got := ansi.Strip(s.Render()); got != "3333\n444X\n    \n    " {
				t.Errorf("next glyph rendered=%q, want=%q", got, "3333\n444X\n    \n    ")
			}
		})
	}
}
