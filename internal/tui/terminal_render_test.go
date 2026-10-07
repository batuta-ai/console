package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

func terminalTestModel() model {
	return model{screen: scrRoom, termMode: true, width: 4, height: 20, sessID: "selected",
		sessions: []domain.Session{{ID: "selected", Cols: 4, Rows: 2}}}
}

func terminalTestFeed(m model, data string) model {
	next, cmd := m.Update(ptyOutMsg(data))
	if cmd != nil {
		if msg := cmd(); msg != nil {
			next, _ = next.Update(msg)
		}
	}
	return next.(model)
}

func TestTerminalViewTwoRowScroll(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"autowrap", "1234\r\nabcdX"},
		{"line_feed", "1234\r\nabcd\r\nX"},
		{"index", "1234\r\nabcd\r\x1bDX"},
		{"scroll_up", "1234\r\nabcd\r\x1b[SX"},
	} {
		for split := 0; split <= len(tc.input); split++ {
			t.Run(fmt.Sprintf("%s/split%d", tc.name, split), func(t *testing.T) {
				m := terminalTestFeed(terminalTestModel(), tc.input[:split])
				m = terminalTestFeed(m, tc.input[split:])
				got := ansi.Strip(m.View().Content)
				if !strings.Contains(got, "\nabcd\nX   \n") {
					t.Fatalf("missing literal complete 4x2 screen abcd / X___: %q", got)
				}
			})
		}
	}
}

func TestTerminalViewSplitUnicodeScroll(t *testing.T) {
	for _, mode := range []string{"\x1b[?2027h", "\x1b[?2027l"} {
		for _, glyph := range []string{"e\u0301", "世", "👩‍💻", "🇧🇷"} {
			input := mode + "1234\r\nabcd" + glyph + "X"
			row := glyph + "X" + strings.Repeat(" ", 4-uniseg.StringWidth(glyph+"X"))
			for split := 0; split <= len(input); split++ {
				t.Run(fmt.Sprintf("%q/%s/split%d", mode, glyph, split), func(t *testing.T) {
					m := terminalTestFeed(terminalTestModel(), input[:split])
					m = terminalTestFeed(m, input[split:])
					got := ansi.Strip(m.View().Content)
					if !strings.Contains(got, "\nabcd\n"+row+"\n") {
						t.Fatalf("missing literal screen abcd / %q: %q", row, got)
					}
				})
			}
		}
	}
}

// Both paths apply the same supported 4x4 -> 4x2 -> 4x4 crop policy.
// Returning from alternate mode must restore the resized main cursor as well
// as its cells: the next character belongs after 444, not on a blank row.
func TestTerminalViewAlternateResizeCursor(t *testing.T) {
	for _, mode := range []string{"", "1047", "1049"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			m := terminalTestModel()
			m.sessions[0].Rows = 4
			m = terminalTestFeed(m, "1111\r\n2222\r\n3333\r\n444")
			if mode != "" {
				m = terminalTestFeed(m, "\x1b[?"+mode+"hALT")
			}
			m.sessions[0].Rows = 2
			m = terminalTestFeed(m, "")
			m.sessions[0].Rows = 4
			m = terminalTestFeed(m, "")
			if mode != "" {
				m = terminalTestFeed(m, "\x1b[?"+mode+"l")
			}
			before := ansi.Strip(m.View().Content)
			if !strings.Contains(before, "\n3333\n444 \n    \n    \n") {
				t.Errorf("main cells not restored: %q", before)
			}
			if m.termFrame.cursorRow != 1 || m.termFrame.cursorCol != 3 {
				t.Errorf("restored cursor=(%d,%d), want=(1,3)", m.termFrame.cursorRow, m.termFrame.cursorCol)
			}
			m = terminalTestFeed(m, "X")
			got := ansi.Strip(m.View().Content)
			if !strings.Contains(got, "\n3333\n444X\n    \n    \n") {
				t.Errorf("next printable must follow resized main cursor; want rows 3333 / 444X / ____ / ____, got %q", got)
			}
		})
	}
}
