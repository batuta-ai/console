package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTerminalViewSynchronizedHold(t *testing.T) {
	m := terminalTestFeed(terminalTestModel(), "A")
	m = terminalTestFeed(m, "B\x1b[?2026hC")
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "\nAB  \n    \n") || !strings.Contains(got, "synchronized output held") {
		t.Fatalf("hold must commit AB and label it: %q", got)
	}
	m.sessions[0].Rows = 4
	m = terminalTestFeed(m, "")
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "\nAB  \n    \n") || strings.Contains(got, "ABC") {
		t.Fatalf("resize exposed partial held frame: %q", got)
	}
	m = terminalTestFeed(m, "\x1b[?2026l")
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "ABC ") || strings.Contains(got, "synchronized output held") {
		t.Fatalf("reset must commit ABC: %q", got)
	}
}

func TestTerminalViewResetAndHiddenCursor(t *testing.T) {
	for _, tc := range []struct {
		input   string
		visible bool
	}{{"", true}, {"\x1bc", true}, {"\x1b[?25l", false}, {"\x1b[?25l\x1bc", true}} {
		t.Run(fmt.Sprintf("%q", tc.input), func(t *testing.T) {
			m := terminalTestFeed(terminalTestModel(), tc.input)
			if got := strings.Contains(m.View().Content, "\x1b[7m"); got != tc.visible {
				t.Fatalf("cursor visible=%v want=%v: %q", got, tc.visible, m.View().Content)
			}
		})
	}
}

func TestTerminalViewStreamLimits(t *testing.T) {
	for _, tc := range []struct{ name, input, problem string }{
		{"grapheme", "e" + strings.Repeat("\u0301", 31), "grapheme"},
		{"csi", "\x1b[" + strings.Repeat("0", 4094), "CSI"},
		{"osc", "\x1b]" + strings.Repeat("a", 65534), "OSC"},
		{"ignored", "\x1bP" + strings.Repeat("a", 65534), "string"},
		{"rep", "a\x1b[+99999b", "REP"},
		{"unicode_rep", "é\x1b[2b", "REP"},
		{"last_column_rep", "abc\x1b[2b", "REP"},
		{"tab", "\x1b[501I", "tab"},
		{"sync", "\x1b[?2026h" + strings.Repeat("a", (1<<20)+1), "synchronized-output limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := terminalTestFeed(terminalTestModel(), tc.input)
			if got := m.View().Content; !strings.Contains(got, tc.problem) {
				t.Fatalf("missing %s limit, frame problem=%q", tc.problem, m.termFrame.problem)
			}
		})
	}
}

func TestTerminalHoldLimitFreezesCommittedBoundary(t *testing.T) {
	m := terminalTestFeed(terminalTestModel(), "AB\x1b[?2026h"+strings.Repeat("x", (1<<20)+1))
	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "\nAB  \n    \n") || !strings.Contains(got, "synchronized output held") {
		t.Fatalf("limit lost committed boundary or held state: %q", got)
	}
}
func TestTerminalChildGlyphsPreserved(t *testing.T) {
	t.Setenv("BATUTA_ASCII", "1")
	m := terminalTestFeed(terminalTestModel(), "—✓─")
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "—✓─ ") {
		t.Fatalf("panel ASCII policy changed child cells: %q", got)
	}
}

func TestTerminalActorTurnBounds(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{{strings.Repeat("a", 1024), 256}, {"\x1b[2JAB", 4}, {"\r\nX", 1}} {
		var s terminalScreen
		n := s.feedTurn(context.Background(), 80, 24, tc.input)
		if n != tc.want {
			t.Fatalf("actor turn consumed %d bytes; want %d", n, tc.want)
		}
	}
}

func TestTerminalGraphemeLimitAcrossCursorAndWrap(t *testing.T) {
	marks := strings.Repeat("\u0301", 31)
	for _, input := range []string{
		"\x1b[2;4He" + marks,
		"\x1b[?7l\x1b[2;3He" + marks,
		"\x1b[2;3H世" + marks,
		"\x1b[2;4H♥\ufe0f" + marks,
		"\x1b[2;2H😀\ufe0e" + marks,
		"e" + marks[:len(marks)-2] + "\x1b[1;2H\u0301",
	} {
		for split := 0; split <= len(input); split++ {
			var s terminalScreen
			s.feed(4, 2, input[:split])
			frame := s.feed(4, 2, input[split:])
			if !strings.Contains(frame.problem, "grapheme") {
				t.Fatalf("lost grapheme bound at split %d in %q: %+v", split, input, frame)
			}
		}
	}
}
