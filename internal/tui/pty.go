package tui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
)

type ptyOutMsg string

type attachedMsg struct {
	id, clientID string
	generation   uint64
	link         *termLink
	err          error
}

func (m model) attach() tea.Cmd {
	return func() tea.Msg {
		result := attachedMsg{id: m.sessID, clientID: m.clientID, generation: m.generation}
		if m.sessID == "" {
			result.err = fmt.Errorf("select an actual session before viewing")
			return result
		}
		lifetime := m.lifetime
		if lifetime == nil {
			lifetime = &terminalLifetime{}
		}
		parent, finish, ok := lifetime.begin()
		if !ok {
			result.err = context.Canceled
			return result
		}
		defer finish()
		ctx, cancel := context.WithCancel(parent)
		request := &termLink{api: m.api, id: m.sessID, clientID: m.clientID}
		selected, err := request.session(ctx)
		if err == nil && selected.Process != "running" {
			err = fmt.Errorf("selected session is %s", selected.Process)
		}
		var stream *client.Terminal
		if err == nil {
			stream, err = m.api.DialTerminalV1(ctx, m.sessID, m.clientID)
		}
		if err != nil {
			cancel()
			result.err = err
			return result
		}
		result.link = newTermLink(m.api, m.sessID, m.clientID, m.generation, ctx, cancel, stream)
		lifetime.add(result.link)
		return result
	}
}
func (m model) waitPTY() tea.Cmd {
	if m.link == nil {
		return nil
	}
	return func() tea.Msg { return m.link.next() }
}
func (m model) sendPTY(text string) tea.Cmd {
	if m.link == nil || text == "" {
		return nil
	}
	key := tea.KeyPressMsg{Text: text}
	m.link.offer(terminalAction{key: &key})
	return nil
}
func (m model) sendResize(cols, rows int) tea.Cmd {
	if m.link != nil {
		m.link.offer(terminalAction{cols: cols, rows: rows})
	}
	return nil
}
func (m model) detach() tea.Cmd {
	link := m.link
	if link == nil {
		return nil
	}
	link.cancel()
	return func() tea.Msg { <-link.done; return link.next() }
}
func clientID() string { return fmt.Sprintf("tui-%d", os.Getpid()) }

func keyToPTY(msg tea.KeyPressMsg) string {
	data, _ := encodeTerminalKey(msg, terminalObserver{})
	return string(data)
}
