package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	portalis "github.com/batuta-ai/console/internal/terminalengine"
	"github.com/charmbracelet/x/ansi"
)

type terminalFrame struct {
	text                 string
	cols, rows           int
	cursorRow, cursorCol int
	problem              string
	held                 bool
	disconnected         bool
	owned                bool
	owner                string
	control              string
	provisional          bool
}

type terminalScreen struct {
	mu          sync.Mutex
	screen      *portalis.Screen
	parser      *portalis.Parser
	frame       terminalFrame
	observer    terminalObserver
	committed   terminalFrame
	failed      bool
	unsupported bool
}

func (s *terminalScreen) feed(cols, rows int, data string) terminalFrame {
	return s.feedContext(context.Background(), cols, rows, data)
}

func (s *terminalScreen) feedContext(ctx context.Context, cols, rows int, data string) terminalFrame {
	for {
		n := s.feedTurn(ctx, cols, rows, data)
		if n >= len(data) || n == 0 {
			break
		}
		data = data[n:]
	}
	return s.present()
}

func (s *terminalScreen) prepare(cols, rows int) bool {
	if s.failed {
		return false
	}
	if cols < 1 || cols > 500 || rows < 2 || rows > 200 {
		s.unsupported = true
		s.frame.problem = fmt.Sprintf("unsupported child geometry %dx%d; screen frozen", cols, rows)
		return false
	}
	if s.screen == nil || s.unsupported {
		s.screen = portalis.NewScreen(rows, cols)
		if s.screen.Rows() != rows || s.screen.Cols() != cols {
			s.unsupported = true
			s.screen = nil
			s.frame.problem = "unsupported child geometry; screen frozen"
			return false
		}
		s.screen.SetScrollbackLimit(400)
		s.screen.SetCursorBlinkVisible(true)
		s.parser = portalis.NewParser(s.screen)
		s.observer = terminalObserver{}
		s.unsupported = false
	} else if s.screen.Rows() != rows || s.screen.Cols() != cols {
		if err := s.parser.ResizeChecked(rows, cols); err != nil {
			s.unsupported = true
			s.frame.problem = "unsupported child resize; screen frozen"
			return false
		}
		s.screen.SetCursorBlinkVisible(true)
	}
	return true
}

func (s *terminalScreen) feedTurn(ctx context.Context, cols, rows int, data string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prepare(cols, rows) {
		if s.observer.held {
			s.observer.holdBytes += len(data)
			if s.observer.holdBytes > 1<<20 {
				s.failed = true
				s.frame.problem = "synchronized-output limit (1 MiB); screen frozen"
			}
		}
		return len(data)
	}
	printable := 0
	for i := 0; i < len(data); i++ {
		if ctx.Err() != nil {
			return i
		}
		boundary, err := s.observer.byte(data[i], s.screen)
		if err != nil {
			s.failed = true
			s.frame.problem = err.Error()
			return len(data)
		}
		if boundary.commit {
			s.committed = s.snapshot()
			s.committed.held = true
			s.frame = s.committed
		}
		before := s.screen.CursorState()
		s.parser.Feed([]byte{data[i]})
		if boundary.reset {
			s.screen.SetCursorBlinkVisible(true)
		}
		if boundary.printable {
			printable++
			after := s.screen.CursorState()
			// The pinned Put operation changes the current or preceding base
			// cell (at most two columns wide). Check both public cursor
			// positions to include wrapping and width-changing relocation.
			// Previously this reread two entire rows for every rune.
			for _, cursor := range []portalis.Cursor{before, after} {
				for col := max(0, cursor.Col-2); col <= min(s.screen.Cols()-1, cursor.Col+1); col++ {
					cell, _ := s.screen.CellAt(cursor.Row, col)
					if len(cell.Combining) > 60 {
						s.failed = true
						s.frame.problem = "unsupported grapheme (Combining exceeds 60 bytes); screen frozen"
						return len(data)
					}
				}
			}
		}
		if printable == 256 || boundary.operation {
			return i + 1
		}
	}
	return len(data)
}

func (s *terminalScreen) present() terminalFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.unsupported || s.screen == nil {
		return s.frame
	}
	if s.observer.held {
		control, owner := s.frame.control, s.frame.owner
		s.frame = s.committed
		s.frame.held = true
		s.frame.control = control
		s.frame.owner = owner
	} else {
		s.frame = s.snapshot()
	}
	return s.frame
}

func (s *terminalScreen) snapshot() terminalFrame {
	cursor := s.screen.CursorState()
	return terminalFrame{provisional: true, control: s.frame.control, owner: s.frame.owner, text: s.screen.Render(), cols: s.screen.Cols(), rows: s.screen.Rows(), cursorRow: cursor.Row, cursorCol: cursor.Col}
}

func (f terminalFrame) render(cols, rows int) string {
	if cols < 1 || rows < 1 {
		return ""
	}
	lines := strings.Split(f.text, "\n")
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, cols, "") + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

type terminalFrameMsg struct {
	sessionID  string
	clientID   string
	generation uint64
	frame      terminalFrame
}
