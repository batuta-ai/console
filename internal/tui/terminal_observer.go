package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	engine "github.com/batuta-ai/console/internal/terminalengine"
)

// This observer follows the pinned parser's boundaries, including private
// h/l markers and ESC bytes inside CSI. It owns no cell or cursor state.
type terminalObserver struct {
	state                                      int
	count                                      int
	seq                                        []byte
	pending                                    []byte
	previous                                   byte
	alternate, application, paste, focus, held bool
	holdBytes                                  int
}

const (
	observeNormal = iota
	observeEscape
	observeIntermediate
	observeCSI
	observeOSC
	observeString
	observeStringEscape
)

type terminalBoundary struct{ printable, reset, commit, operation bool }

func (o *terminalObserver) byte(b byte, s *engine.Screen) (terminalBoundary, error) {
	var result terminalBoundary
	originalState := o.state
	if o.held {
		o.holdBytes++
		if o.holdBytes > 1<<20 {
			return result, fmt.Errorf("synchronized-output limit (1 MiB); screen frozen")
		}
	}
	switch o.state {
	case observeNormal:
		if b < 0x80 && len(o.pending) > 0 {
			o.pending = nil
			result.printable = true
		}
		if b == 0x1b {
			o.state = observeEscape
			o.count = 1
			break
		}
		if len(o.pending) == 0 {
			switch b {
			case 0x9b:
				o.state = observeCSI
				o.count = 1
				o.seq = o.seq[:0]
				return result, nil
			case 0x9d:
				o.state = observeOSC
				o.count = 1
				o.previous = 0
				return result, nil
			case 0x90, 0x98, 0x9e, 0x9f:
				o.state = observeString
				o.count = 1
				return result, nil
			}
		}
		if b >= 0x80 {
			o.pending = append(o.pending, b)
			if utf8.FullRune(o.pending) {
				result.printable = true
				o.pending = o.pending[:0]
			}
		} else if b >= 0x20 && b != 0x7f {
			result.printable = true
		}
	case observeEscape:
		switch b {
		case '[':
			o.state = observeCSI
			o.count = 2
			o.seq = o.seq[:0]
		case ']':
			o.state = observeOSC
			o.count = 2
			o.previous = 0
		case 'P', 'X', '^', '_':
			o.state = observeString
			o.count = 2
		case 'c':
			*o = terminalObserver{}
			result.reset = true
		default:
			o.state = observeNormal
			if b >= 0x20 && b <= 0x2f {
				o.state = observeIntermediate
			}
		}
	case observeIntermediate:
		if b >= 0x30 && b <= 0x7e {
			o.state = observeNormal
		}
	case observeCSI:
		o.count++
		final := b >= 0x40 && b <= 0x7e
		if o.count > 4096 || o.count == 4096 && !final {
			return result, fmt.Errorf("CSI limit (4096 bytes); screen frozen")
		}
		if final {
			var err error
			result, err = o.csi(b, s)
			if err != nil {
				return result, err
			}
			o.state = observeNormal
			o.seq = o.seq[:0]
		} else {
			o.seq = append(o.seq, b)
		}
	case observeOSC:
		o.count++
		final := b == 7 || b == 0x9c || b == '\\' && o.previous == 0x1b
		if o.count > 65536 || o.count == 65536 && !final {
			return result, fmt.Errorf("OSC limit (64 KiB); screen frozen")
		}
		if final {
			o.state = observeNormal
		}
		o.previous = b
	case observeString, observeStringEscape:
		o.count++
		final := o.state == observeString && b == 0x9c || o.state == observeStringEscape && b == '\\'
		if o.count > 65536 || o.count == 65536 && !final {
			return result, fmt.Errorf("ignored string limit (64 KiB); screen frozen")
		}
		if final {
			o.state = observeNormal
		} else if b == 0x1b {
			o.state = observeStringEscape
		} else {
			o.state = observeString
		}
	}
	result.operation = originalState != observeNormal && o.state == observeNormal || originalState == observeNormal && b < 0x20 && b != 0x1b
	return result, nil
}

func (o *terminalObserver) csi(final byte, s *engine.Screen) (terminalBoundary, error) {
	var result terminalBoundary
	raw := string(o.seq)
	private := byte(0)
	if raw != "" && strings.ContainsRune("?><=!", rune(raw[0])) {
		private = raw[0]
		raw = raw[1:]
	}
	parts := strings.Split(raw, ";")
	values := make([]int, len(parts))
	overflow := false
	for i, part := range parts {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			if e, ok := err.(*strconv.NumError); ok && e.Err == strconv.ErrRange {
				overflow = true
			}
			continue
		}
		values[i] = n
	}
	if private == 0 && (final == 'b' || final == 'I' || final == 'Z') {
		n := values[0]
		if n <= 0 {
			n = 1
		}
		if final == 'b' {
			if overflow || n > 10000 {
				return result, fmt.Errorf("REP limit (10000); screen frozen")
			}
			c := s.CursorState()
			if c.Col == s.Cols()-1 {
				return result, fmt.Errorf("REP at last column unsupported; screen frozen")
			}
			cell, ok := s.CellAt(c.Row, c.Col-1)
			if cell.Continuation {
				cell, ok = s.CellAt(c.Row, c.Col-2)
			}
			if !ok || cell.Rune < 0x20 || cell.Rune > 0x7e || cell.Combining != "" {
				return result, fmt.Errorf("REP requires plain ASCII; screen frozen")
			}
		} else if overflow || n > 500 {
			return result, fmt.Errorf("tab count limit (500); screen frozen")
		}
	}
	if final == 'p' && private == '!' {
		*o = terminalObserver{}
		result.reset = true
		return result, nil
	}
	if (final == 'h' || final == 'l') && private != 0 {
		active := final == 'h'
		for _, mode := range values {
			switch mode {
			case 1:
				o.application = active
			case 1047, 1049:
				o.alternate = active
			case 1004:
				o.focus = active
			case 2004:
				o.paste = active
			case 2026:
				if active && !o.held {
					result.commit = true
					o.holdBytes = 0
				}
				o.held = active
			}
		}
	}
	return result, nil
}
