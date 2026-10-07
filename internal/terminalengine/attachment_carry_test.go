package terminalengine

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestAttachmentMixed1049And1048FragmentAcrossCrop(t *testing.T) {
	restore := "\x1b[?1048l"
	for split := 0; split <= len(restore); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			s := NewScreen(4, 4)
			p := NewParser(s)
			p.Feed([]byte("1111\r\n2222\r\n3333\r\n444\x1b[?1048h\x1b[?1049hALT"))
			p.Feed([]byte(restore[:split]))
			if err := p.ResizeChecked(2, 4); err != nil {
				t.Fatal(err)
			}
			if len(s.savedCells) != 2 || s.savedCells[0][0].Rune != '3' || s.savedCells[1][0].Rune != '4' {
				t.Fatal("fixture did not crop the saved main grid")
			}
			if len(s.scrollback) != 2 || s.scrollback[0][0].Rune != '1' || s.scrollback[1][0].Rune != '2' {
				t.Fatal("fixture did not retain the two actual cropped main rows")
			}
			p.Feed([]byte(restore[split:]))
			if c := s.CursorState(); c.Row != 1 || c.Col != 3 {
				t.Fatalf("1048 restored cursor=%+v", c)
			}
			if err := p.ResizeChecked(4, 4); err != nil {
				t.Fatal(err)
			}
			p.Feed([]byte("\x1b[?1049lX"))
			if got := ansi.Strip(s.Render()); got != "3333\n444X\n    \n    " {
				t.Fatalf("1049 crop restore=%q", got)
			}
		})
	}
}
func TestAttachmentResetClearsSavedAlternateMarkers(t *testing.T) {
	for _, reset := range []string{"\x1bc", "\x1b[!p"} {
		t.Run(fmt.Sprintf("%q", reset), func(t *testing.T) {
			s := NewScreen(4, 4)
			p := NewParser(s)
			p.Feed([]byte("MAIN\x1b[?1049h\x1b[4;4H\x1b[?1048h\x1b[?1049l"))
			if !s.savedCursorAlt || !s.savedCursorDiscarded {
				t.Fatal("fixture failed to set both saved alternate markers")
			}
			p.Feed([]byte(reset))
			if s.savedCursorAlt || s.savedCursorDiscarded {
				t.Fatalf("reset retained markers: alternate=%v discarded=%v", s.savedCursorAlt, s.savedCursorDiscarded)
			}
			p.Feed([]byte("\x1b[?1049h\x1b[4;1H"))
			if err := p.ResizeChecked(2, 4); err != nil {
				t.Fatal(err)
			}
			p.Feed([]byte("\x1b[?1048l"))
			if c := s.CursorState(); c.Row != 0 || c.Col != 0 {
				t.Fatalf("reset cursor metadata revived: %+v", c)
			}
		})
	}
}
