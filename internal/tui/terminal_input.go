package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func encodeTerminalPaste(text string, bracketed bool) ([]byte, error) {
	overhead := 0
	if bracketed {
		overhead = 12
	}
	if len(text) > terminalPendingLimit-overhead {
		return nil, fmt.Errorf("paste exceeds 1 MiB; nothing sent")
	}
	if bracketed {
		text = "\x1b[200~" + text + "\x1b[201~"
	}
	return []byte(text), nil
}
func encodeTerminalKey(key tea.KeyPressMsg, modes terminalObserver) ([]byte, error) {
	if key.Mod&^(tea.ModShift|tea.ModAlt|tea.ModCtrl) != 0 {
		return nil, fmt.Errorf("unsupported terminal modifier")
	}
	name := key.String()
	switch name {
	case "ctrl+]":
		return nil, nil
	case "ctrl+c":
		return []byte{3}, nil
	case "ctrl+z":
		return []byte{26}, nil
	case "ctrl+\\":
		return []byte{28}, nil
	case "ctrl+d":
		return []byte{4}, nil
	}
	mod := 1
	if key.Mod&tea.ModShift != 0 {
		mod++
	}
	if key.Mod&tea.ModAlt != 0 {
		mod += 2
	}
	if key.Mod&tea.ModCtrl != 0 {
		mod += 4
	}
	base := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(name, "ctrl+"), "alt+"), "shift+")
	final := ""
	switch base {
	case "up":
		final = "A"
	case "down":
		final = "B"
	case "right":
		final = "C"
	case "left":
		final = "D"
	case "home":
		final = "H"
	case "end":
		final = "F"
	}
	if final != "" {
		if mod > 1 {
			return []byte(fmt.Sprintf("\x1b[1;%d%s", mod, final)), nil
		}
		if modes.application {
			return []byte("\x1bO" + final), nil
		}
		return []byte("\x1b[" + final), nil
	}
	number := 0
	switch base {
	case "insert":
		number = 2
	case "delete":
		number = 3
	case "pgup":
		number = 5
	case "pgdown":
		number = 6
	}
	if number != 0 {
		if mod > 1 {
			return []byte(fmt.Sprintf("\x1b[%d;%d~", number, mod)), nil
		}
		return []byte(fmt.Sprintf("\x1b[%d~", number)), nil
	}
	switch name {
	case "enter":
		return []byte("\r"), nil
	case "tab":
		return []byte("\t"), nil
	case "shift+tab":
		return []byte("\x1b[Z"), nil
	case "esc":
		return []byte("\x1b"), nil
	case "backspace":
		return []byte{127}, nil
	}
	if key.Mod&tea.ModCtrl != 0 && key.Code >= 0x40 && key.Code <= 0x7f {
		data := []byte{byte(key.Code) & 0x1f}
		if key.Mod&tea.ModAlt != 0 {
			data = append([]byte{27}, data...)
		}
		return data, nil
	}
	text := key.Text
	if text == "" && key.Code >= 32 && key.Code < 127 {
		text = string(key.Code)
	}
	if text != "" {
		if len(text) > terminalPendingLimit {
			return nil, fmt.Errorf("terminal input exceeds 1 MiB")
		}
		if key.Mod&tea.ModAlt != 0 {
			text = "\x1b" + text
		}
		return []byte(text), nil
	}
	return nil, fmt.Errorf("unsupported terminal key")
}
