package tui

import (
	"reflect"
	"strings"
	"testing"

	engine "github.com/batuta-ai/console/internal/terminalengine"
)

func TestTerminalObserverMatchesPinnedModes(t *testing.T) {
	streams := []string{
		"\x1b[?1;1004;2004;2026h\x1b[?1049h\x1b[?1048h\x1b[?1047l\x1b[?1048l\x1b[?1049l\x1b[?2026l",
		"\x1b[?47h\x1b[?1049h\x1b[!p",
		"\x1b[?1047;2026h\x1bc",
		"\xe4\x9b?2026h\x1b[?1004\x1bh",
		"\x1bP\x1b[?2026h\x1b\\X\x1b]2;\x1b[?2026h\x07",
	}
	for _, marker := range []string{"?", ">", "<", "=", "!"} {
		for _, params := range []string{"1;1004;2004;1049;2026", "+1;+1004;+2004;+1047;+2026", "0;", "-1;999999999999999999999", " 1;2026 "} {
			streams = append(streams, "\x1b["+marker+params+"h\x1b["+marker+params+"l")
		}
	}
	for _, input := range streams {
		for split := 0; split <= len(input); split++ {
			s := engine.NewScreen(3, 8)
			p := engine.NewParser(s)
			var o terminalObserver
			for _, part := range []string{input[:split], input[split:]} {
				for _, b := range []byte(part) {
					if _, err := o.byte(b, s); err != nil {
						t.Fatal(err)
					}
					p.Feed([]byte{b})
					v := reflect.ValueOf(s).Elem()
					for name, got := range map[string]bool{"altScreen": o.alternate, "applicationCursor": o.application, "bracketedPaste": o.paste, "focusReporting": o.focus, "syncActive": o.held} {
						if want := v.FieldByName(name).Bool(); got != want {
							t.Fatalf("%q split=%d mode=%s got=%v pinned=%v", input, split, name, got, want)
						}
					}
				}
			}
		}
	}
}
func TestTerminalControlStringExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, ending string
		limit                int
	}{
		{"CSI7", "\x1b[", "m", 4096}, {"CSI8", "\x9b", "m", 4096},
		{"OSC7", "\x1b]", "\x1b\\", 65536}, {"OSC8", "\x9d", "\x9c", 65536},
		{"DCS7", "\x1bP", "\x1b\\", 65536}, {"DCS8", "\x90", "\x9c", 65536},
		{"SOS7", "\x1bX", "\x1b\\", 65536}, {"SOS8", "\x98", "\x9c", 65536},
		{"PM7", "\x1b^", "\x1b\\", 65536}, {"PM8", "\x9e", "\x9c", 65536},
		{"APC7", "\x1b_", "\x1b\\", 65536}, {"APC8", "\x9f", "\x9c", 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.prefix + strings.Repeat("0", tc.limit-len(tc.prefix)-len(tc.ending)) + tc.ending
			for _, split := range []int{0, len(tc.prefix), len(input) - 1, len(input)} {
				var s terminalScreen
				s.feed(4, 2, input[:split])
				f := s.feed(4, 2, input[split:])
				if f.problem != "" {
					t.Fatalf("exact bound rejected: %s", f.problem)
				}
			}
			var s terminalScreen
			f := s.feed(4, 2, tc.prefix+strings.Repeat("0", tc.limit-len(tc.prefix))+tc.ending)
			if f.problem == "" {
				t.Fatal("first unterminated boundary accepted")
			}
		})
	}
}

func FuzzTerminalObserverPinned(f *testing.F) {
	for _, seed := range []string{"\x1b[?1049h\x1b[?1048h\x1b[?1047l\x1b[!p", "\xe4\x9b?2026h", "\x1b[?2026h\x1bc", "\x1bP\x1b\x9cX", "\x1b[>+1;+2026h"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 65536 {
			return
		}
		s := engine.NewScreen(3, 8)
		p := engine.NewParser(s)
		var o terminalObserver
		for i, b := range input {
			if _, err := o.byte(b, s); err != nil {
				return
			}
			p.Feed([]byte{b})
			if got := int(reflect.ValueOf(p).Elem().FieldByName("state").Int()); got != o.state {
				t.Fatalf("byte %d parser state %d observer %d input=%x", i, got, o.state, input)
			}
			v := reflect.ValueOf(s).Elem()
			for name, got := range map[string]bool{"altScreen": o.alternate, "applicationCursor": o.application, "bracketedPaste": o.paste, "focusReporting": o.focus, "syncActive": o.held} {
				if want := v.FieldByName(name).Bool(); got != want {
					t.Fatalf("byte %d %s=%v pinned=%v input=%x", i, name, got, want, input)
				}
			}
		}
	})
}
