package terminalengine

import (
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	upstream "github.com/starframe-dev/portalis"
	"reflect"
	"testing"
)

func TestConsoleNoResizeUpstreamParity(t *testing.T) {
	for _, input := range []string{
		"AB\x1b[?1049hALT\x1b[?1049lX",
		"AB\x1b[?1049hALT\x1b[?1049hZ\x1b[?1049lX\x1b[?1049lY",
		"AB\x1b[?1049hALT\x1b[?1047lCD\x1b[?1049lX",
		"\x1b(0qq\x1b[?1049h\x1b(Babc\x1b[?1049lq",
		"AB\x1b[?1049h\x1b[?1048hALT\x1b[?1048lZ\x1b[?1049lX",
		"\x1b[31;1mAB\x1b[?1049hALT\x1b[32mC\x1b[?1049lX",
		"é世👩‍💻🇧🇷\x1b[?2027hA\x1b[?2027lB",
		"AB\x1b[?2026hC\x1b[!pD\x1b[?2026hE\x1bcF",
	} {
		for split := 0; split <= len(input); split++ {
			t.Run(fmt.Sprintf("%q/split%d", input, split), func(t *testing.T) {
				s := NewScreen(4, 12)
				p := NewParser(s)
				u := upstream.NewScreen(4, 12)
				q := upstream.NewParser(u)
				s.SetCursorBlinkVisible(true)
				u.SetCursorBlinkVisible(true)
				for _, chunk := range []string{input[:split], input[split:]} {
					p.Feed([]byte(chunk))
					q.Feed([]byte(chunk))
				}
				a, _ := json.Marshal(s.CellsSnapshot())
				b, _ := json.Marshal(u.CellsSnapshot())
				c, _ := json.Marshal(s.CursorState())
				d, _ := json.Marshal(u.CursorState())
				if string(a) != string(b) || string(c) != string(d) || s.Render() != u.Render() {
					t.Fatal("headless subset changed upstream non-resize semantics")
				}
			})
		}
	}
}

func TestConsole1049ResizeMainStateMatrix(t *testing.T) {
	for _, prefix := range []string{
		"1111\r\n2222\r\n3333\r\n444",
		"1111\r\n2222\r\n3333\r\n4444",
		"1111\r\n2222\r\n3333\r\n444\x1b[31;1m",
		"1111\r\n2222\r\n3333\r\n444\x1b(0",
		"1111\r\n2222\r\n3333\r\n444\x1b[?7l",
		"1111\r\n2222\r\n3333\r\n444\x1b[2;4r\x1b[?6h\x1b[3;4H",
	} {
		for _, geometry := range [][][2]int{{{2, 4}, {4, 4}}, {{2, 3}, {4, 4}}, {{6, 8}, {2, 2}, {4, 4}}, {{4, 4}}} {
			t.Run(fmt.Sprintf("%q/%v", prefix, geometry), func(t *testing.T) {
				main := NewScreen(4, 4)
				mp := NewParser(main)
				s := NewScreen(4, 4)
				p := NewParser(s)
				mp.Feed([]byte(prefix))
				p.Feed([]byte(prefix + "\x1b[?1049hALT"))
				for _, size := range geometry {
					if err := mp.ResizeChecked(size[0], size[1]); err != nil {
						t.Fatal(err)
					}
					if err := p.ResizeChecked(size[0], size[1]); err != nil {
						t.Fatal(err)
					}
				}
				p.Feed([]byte("\x1b[?1049l"))
				if !reflect.DeepEqual(main.cells, s.cells) || main.cursorState() != s.cursorState() {
					t.Fatalf("restored main differs got=%+v want=%+v", s.cursorState(), main.cursorState())
				}
				mp.Feed([]byte("qX"))
				p.Feed([]byte("qX"))
				if !reflect.DeepEqual(main.cells, s.cells) || main.cursorState() != s.cursorState() {
					t.Fatal("next glyph/charset/wrap/style differs")
				}
			})
		}
	}
}

func TestConsoleParserFragmentSurvivesResize(t *testing.T) {
	for _, input := range []string{"é世👩‍💻🇧🇷", "\x1b[31;1mAB", "\x1b]2;title\x1b\\AB"} {
		for split := 0; split <= len(input); split++ {
			t.Run(fmt.Sprintf("%q/%d", input, split), func(t *testing.T) {
				s := NewScreen(4, 12)
				p := NewParser(s)
				want := NewScreen(6, 14)
				q := NewParser(want)
				p.Feed([]byte(input[:split]))
				if err := p.ResizeChecked(6, 14); err != nil {
					t.Fatal(err)
				}
				p.Feed([]byte(input[split:]))
				q.Feed([]byte(input))
				if !reflect.DeepEqual(s.cells, want.cells) || s.cursorState() != want.cursorState() || p.lastTitle != q.lastTitle {
					t.Fatal("resize disturbed partial parser state")
				}
			})
		}
	}
}

func TestConsoleMixedModesResizeLiteral(t *testing.T) {
	for _, tc := range []struct {
		prefix, expected string
		row, col         int
	}{
		{"1111\r\n2222\r\n3333\r\n444\x1b[?1049hALT\x1b[?1047l\x1b[1;1H\x1b[?1047hALT", "1111\n222X\n    \n    ", 1, 3},
		{"1111\r\n2222\r\n3333\r\n444\x1b[1;1H\x1b[?1049hALT\x1b[?1047l\x1b[4;4H\x1b[?1047hALT", "X333\n444 \n    \n    ", 0, 0},
	} {
		for split := 0; split <= len(tc.prefix); split++ {
			t.Run(fmt.Sprintf("%d/%d", tc.row, split), func(t *testing.T) {
				s := NewScreen(4, 4)
				s.SetCursorBlinkVisible(true)
				p := NewParser(s)
				p.Feed([]byte(tc.prefix[:split]))
				p.Feed([]byte(tc.prefix[split:]))
				for _, rows := range []int{2, 4} {
					if err := p.ResizeChecked(rows, 4); err != nil {
						t.Fatal(err)
					}
				}
				p.Feed([]byte("\x1b[?1049l"))
				if c := s.CursorState(); c.Row != tc.row || c.Col != tc.col {
					t.Fatalf("mixed restore cursor=%+v want(%d,%d)", c, tc.row, tc.col)
				}
				p.Feed([]byte("X"))
				if got := ansi.Strip(s.Render()); got != tc.expected {
					t.Fatalf("mixed resize=%q want%q", got, tc.expected)
				}
			})
		}
	}
}

func TestConsoleMixedModeFragmentAcrossResize(t *testing.T) {
	for _, input := range []string{
		"AB\x1b[?1049hALT\x1b[?1049lX",
		"AB\x1b[?1049hALT\x1b[?1047lCD\x1b[?1049lX",
		"AB\x1b[?1049h\x1b[?1048hALT\x1b[?1048lZ\x1b[?1049lX",
	} {
		for split := 0; split <= len(input); split++ {
			t.Run(fmt.Sprintf("%q/%d", input, split), func(t *testing.T) {
				s := NewScreen(4, 12)
				p := NewParser(s)
				want := NewScreen(6, 14)
				q := NewParser(want)
				p.Feed([]byte(input[:split]))
				if err := p.ResizeChecked(6, 14); err != nil {
					t.Fatal(err)
				}
				p.Feed([]byte(input[split:]))
				q.Feed([]byte(input))
				if !reflect.DeepEqual(s.cells, want.cells) || s.cursorState() != want.cursorState() || s.savedCursor != want.savedCursor {
					t.Fatal("mixed-mode sequence spanning resize lost parser/cursor state")
				}
			})
		}
	}
}

func TestConsoleSavedCursorResize(t *testing.T) {
	for _, pair := range [][2]string{{"\x1b7", "\x1b8"}, {"\x1b[?1048h", "\x1b[?1048l"}} {
		for _, alt := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/alt%v", pair[0], alt), func(t *testing.T) {
				prefix := "1111\r\n2222\r\n3333\r\n444"
				main := NewScreen(4, 4)
				mp := NewParser(main)
				mp.Feed([]byte(prefix))
				s := NewScreen(4, 4)
				p := NewParser(s)
				p.Feed([]byte(prefix + pair[0]))
				if alt {
					p.Feed([]byte("\x1b[?1049hALT"))
				}
				for _, rows := range []int{2, 4} {
					if err := p.ResizeChecked(rows, 4); err != nil {
						t.Fatal(err)
					}
					if err := mp.ResizeChecked(rows, 4); err != nil {
						t.Fatal(err)
					}
				}
				if alt {
					p.Feed([]byte("\x1b[?1049l"))
				}
				p.Feed([]byte(pair[1]))
				if s.cursorState() != main.cursorState() {
					t.Errorf("saved cursor=%+v wantmain%+v", s.cursorState(), main.cursorState())
				}
				p.Feed([]byte("X"))
				mp.Feed([]byte("X"))
				if !reflect.DeepEqual(s.cells, main.cells) {
					t.Fatalf("next saved-cursor glyph differs; got%q want%q", ansi.Strip(s.Render()), ansi.Strip(main.Render()))
				}
			})
		}
	}
}

func TestConsoleSavedAlternateCursorResize(t *testing.T) {
	for _, pair := range [][2]string{{"\x1b7", "\x1b8"}, {"\x1b[?1048h", "\x1b[?1048l"}} {
		for _, discarded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/discarded%v", pair[0], discarded), func(t *testing.T) {
				prefix := "1111\r\n2222\r\n3333\r\n444"
				s := NewScreen(4, 4)
				p := NewParser(s)
				p.Feed([]byte("\x1b[?1049h" + prefix))
				if discarded {
					p.Feed([]byte("\x1b[2;4H"))
				}
				p.Feed([]byte(pair[0]))
				if discarded {
					p.Feed([]byte("\x1b[?1049l" + prefix))
				}
				for _, rows := range []int{2, 4} {
					if err := p.ResizeChecked(rows, 4); err != nil {
						t.Fatal(err)
					}
				}
				p.Feed([]byte(pair[1] + "X"))
				if got := ansi.Strip(s.Render()); got != "3333\n444X\n    \n    " {
					t.Fatalf("saved alternate cursor grid=%q", got)
				}
			})
		}
	}
}

func TestConsoleDiscardedSavedCursorAfterAlternateReentry(t *testing.T) {
	s := NewScreen(4, 4)
	p := NewParser(s)
	p.Feed([]byte("\x1b[?1049h\x1b[2;4H\x1b7\x1b[?1049l\x1b[?1047h1111\r\n2222\r\n3333\r\n444"))
	for _, rows := range []int{2, 4} {
		if err := p.ResizeChecked(rows, 4); err != nil {
			t.Fatal(err)
		}
	}
	p.Feed([]byte("\x1b8X"))
	if got := ansi.Strip(s.Render()); got != "3333\n444X\n    \n    " {
		t.Fatalf("discarded saved cursor was rebased by a new alternate grid: %q", got)
	}
}
