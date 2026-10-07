package terminalengine

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func TestConsole1049ResizeMainCursor(t *testing.T) {
	for _, mode := range []string{"", "1047", "1049"} {
		t.Run(mode, func(t *testing.T) {
			s := NewScreen(4, 4)
			s.SetCursorBlinkVisible(true)
			p := NewParser(s)
			p.Feed([]byte("1111\r\n2222\r\n3333\r\n444"))
			if mode != "" {
				p.Feed([]byte("\x1b[?" + mode + "hALT"))
			}
			for _, rows := range []int{2, 4} {
				if err := p.ResizeChecked(rows, 4); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "" {
				p.Feed([]byte("\x1b[?" + mode + "l"))
			}
			if c := s.CursorState(); c.Row != 1 || c.Col != 3 {
				t.Errorf("restored cursor=(%d,%d), want=(1,3)", c.Row, c.Col)
			}
			p.Feed([]byte("X"))
			if got := ansi.Strip(s.Render()); got != "3333\n444X\n    \n    " {
				t.Errorf("next glyph rendered=%q, want=%q", got, "3333\n444X\n    \n    ")
			}
		})
	}
}
func TestConsoleResizeNoOpInvalidPreservesAllState(t *testing.T) {
	for _, input := range []string{
		"AB\x1b(0\x1b)0\x0e\x1b[?1049hALT\x1b[31",
		"AB\x1b[?1049h\x1b[?2026hALT",
		"AB\x1b[?1049h\xe4",
		"AB\x1b[?1049h\x1b]2;title",
		"AB\x1b[?1049h\x1b(",
	} {
		t.Run(input, func(t *testing.T) {
			s := NewScreen(4, 4)
			p := NewParser(s)
			p.Feed([]byte(input))
			// Formatting eagerly captures nested slices and the committed-frame value;
			// saved copies must not share mutable backing arrays with the live state.
			// Builder storage addresses are not parser state; capture its bytes separately.
			snapshot := func() string {
				parserState := *p
				parserState.buf = strings.Builder{}
				return fmt.Sprintf("%#v|%#v|%#v|%q", *s, s.committedFrame, parserState, p.buf.String())
			}
			before := snapshot()
			for _, size := range [][2]int{{4, 4}, {0, 4}, {4, 0}, {65536, 4}, {500, 600}} {
				err := p.ResizeChecked(size[0], size[1])
				if size != [2]int{4, 4} && err == nil {
					t.Fatal("invalid resize accepted")
				}
				if got := snapshot(); before != got {
					t.Fatalf("invalid/no-op resize changed screen/parser state; dimensions=%v\nBEFORE=%s\nAFTER=%s", size, before, got)
				}
			}
		})
	}
}
