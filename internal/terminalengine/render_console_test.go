package terminalengine

import (
	"fmt"
	"strings"
	"testing"

	upstream "github.com/starframe-dev/portalis"
)

func TestConsoleStyledRenderUpstreamParity(t *testing.T) {
	var diverse strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&diverse, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%d;%dmX", i, i+1, i+2, 255-i, 254-i, 253-i, i%10)
	}
	styles := "\x1b[38;2;123;234;255;48;2;231;252;198;1;3;4;9mé世" +
		"\x1b[48;2;255;234;123;22;23;24;29m👩‍💻🇧🇷" +
		"\x1b[8mhidden\x1b[0mplain"
	for _, input := range []string{strings.Repeat(styles, 4), diverse.String(), "\x1b[?1049h" + styles + "\x1b[?1049l" + styles} {
		for _, cursor := range []bool{false, true} {
			s, u := NewScreen(4, 100), upstream.NewScreen(4, 100)
			p, q := NewParser(s), upstream.NewParser(u)
			s.SetCursorBlinkVisible(cursor)
			u.SetCursorBlinkVisible(cursor)
			compare := func(phase string) {
				t.Helper()
				if got, want := s.Render(), u.Render(); got != want {
					t.Fatalf("styled Render bytes differ during %s: got %q want %q", phase, got, want)
				}
			}
			for _, chunk := range []string{input, "\x1b[1;2H", "\x1b[?2026hchanged", "\x1b[?2026l", "\x1b[0m\r\nchanged colors"} {
				p.Feed([]byte(chunk))
				q.Feed([]byte(chunk))
				compare("feed")
				compare("cached screen")
			}
			s.StartSelection(0, 0)
			u.StartSelection(0, 0)
			s.ExtendSelection(2, 50)
			u.ExtendSelection(2, 50)
			compare("selection")
			s.ClearSelection()
			u.ClearSelection()
			compare("selection cleared")
		}
	}
}
