package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/coder/websocket"
)

type ptyOutMsg string

type attachedMsg struct {
	id string
}

type termLink struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	cancel context.CancelFunc
	out    chan string
}

func (m model) attach() tea.Cmd {
	return func() tea.Msg {
		var sessions []domain.Session
		_ = m.api.Get("/v0/sessions", &sessions)
		id := ""
		for _, s := range sessions {
			if s.Process == "running" && (m.missionID == "" || s.MissionID == m.missionID) {
				id = s.ID
				break
			}
		}
		if id == "" && m.missionID != "" {
			var sess domain.Session
			err := m.api.Post("/v0/sessions", map[string]any{
				"mission_id": m.missionID,
				"owner":      "tui",
				"client":     m.clientID,
				"batuta":     true,
			}, &sess)
			if err != nil {
				return ptyOutMsg("cannot start session: " + err.Error() + "\n")
			}
			id = sess.ID
		}
		if id == "" {
			return ptyOutMsg("no running session — open a mission first\n")
		}
		_ = m.api.Post("/v0/sessions/"+id+"/control", map[string]string{"surface": "tui", "client": m.clientID}, &domain.Session{})
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := m.api.DialTerminal(ctx, id, m.clientID)
		if err != nil {
			cancel()
			return ptyOutMsg("terminal: " + err.Error() + "\n")
		}
		link := m.link
		link.mu.Lock()
		if link.cancel != nil {
			link.cancel()
		}
		if link.conn != nil {
			_ = link.conn.CloseNow()
		}
		link.conn = conn
		link.cancel = cancel
		link.mu.Unlock()
		go func() {
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				select {
				case link.out <- string(data):
				case <-ctx.Done():
					return
				}
			}
		}()
		return attachedMsg{id: id}
	}
}

func (m model) waitPTY() tea.Cmd {
	return func() tea.Msg {
		if m.link == nil {
			return nil
		}
		s, ok := <-m.link.out
		if !ok {
			return nil
		}
		return ptyOutMsg(s)
	}
}

func (m model) sendPTY(s string) tea.Cmd {
	return func() tea.Msg {
		if m.link == nil || s == "" {
			return nil
		}
		m.link.mu.Lock()
		conn := m.link.conn
		m.link.mu.Unlock()
		if conn == nil {
			return nil
		}
		_ = conn.Write(context.Background(), websocket.MessageText, []byte(s))
		return nil
	}
}

func (m model) sendResize(cols, rows int) tea.Cmd {
	return func() tea.Msg {
		if m.link == nil || cols < 1 || rows < 1 {
			return nil
		}
		m.link.mu.Lock()
		conn := m.link.conn
		m.link.mu.Unlock()
		if conn == nil {
			return nil
		}
		payload, err := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
		if err != nil {
			return nil
		}
		_ = conn.Write(context.Background(), websocket.MessageText, payload)
		return nil
	}
}

func keyToPTY(msg tea.KeyPressMsg) string {
	switch msg.String() {
	case "enter":
		return "\r"
	case "backspace":
		return "\x7f"
	case "tab":
		return "\t"
	case "ctrl+c":
		return "\x03"
	case "ctrl+d":
		return "\x04"
	case "ctrl+l":
		return "\x0c"
	case "shift+tab":
		return "\x1b[Z"
	case "home":
		return "\x1b[H"
	case "end":
		return "\x1b[F"
	case "delete":
		return "\x1b[3~"
	case "up":
		return "\x1b[A"
	case "down":
		return "\x1b[B"
	case "right":
		return "\x1b[C"
	case "left":
		return "\x1b[D"
	}
	if msg.Text != "" {
		return msg.Text
	}
	return ""
}

func clientID() string {
	return fmt.Sprintf("tui-%d", os.Getpid())
}
