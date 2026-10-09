package tui

import (
	"bytes"
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func terminalRawInputFixture(t *testing.T) (map[string][]string, func(context.Context, *session.TerminalSubscription, *termLink, int) []byte) {
	t.Helper()
	record := filepath.Join(testfixture.PrivateHome(t), "actual-terminal-input.bin")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string][]string{"z-selected": {executable, "-test.run=^TestTerminalNativeRawRecordingChild$", "--", "console-tui-raw-input", record}}
	// ConPTY output is a terminal presentation; observe raw input at the child.
	return commands, func(ctx context.Context, sub *session.TerminalSubscription, link *termLink, _ int) []byte {
		// The delimiter follows every test key through the same ordered queue.
		delimiter := tea.KeyPressMsg{Code: '^', Mod: tea.ModCtrl}
		if !link.offer(terminalAction{key: &delimiter}) {
			t.Fatal("raw-input completion delimiter rejected")
		}
		var output strings.Builder
		for !strings.Contains(output.String(), "RAW-INPUT-CAPTURED") {
			event, err := sub.Next(ctx)
			if err != nil {
				t.Fatalf("actual raw-input handshake missing: %v; presentation=%q", err, output.String())
			}
			output.Write(event.Data)
		}
		got, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("actual native terminal child input=%x", got)
		return got
	}
}

func TestTerminalNativeRawObservationPreservesExtraByte(t *testing.T) {
	commands, observe := terminalRawInputFixture(t)
	m, manager := terminalAttachedFixture(t, commands)
	m.link.offer(terminalAction{operation: "tui"})
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	m.termMode = true
	sub, err := manager.SubscribeTerminal(m.sessID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var replay []byte
	for !strings.Contains(string(replay), "READY") {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		replay = append(replay, event.Data...)
	}
	for _, key := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, {Code: tea.KeyEscape}, {Code: 'c', Mod: tea.ModCtrl}, {Code: 'z', Mod: tea.ModCtrl}, {Code: '\\', Mod: tea.ModCtrl}, {Code: 'd', Mod: tea.ModCtrl}, {Code: 'X', Text: "X"}} {
		next, _ := m.Update(key)
		m = next.(model)
	}
	want := []byte{'q', 27, 3, 26, 28, 4, 'X'}
	got := observe(ctx, sub, m.link, len(want))
	if !bytes.Equal(got, want) {
		t.Fatalf("actual terminal input=%x want=%x; recorder must not truncate extra bytes", got, want)
	}
	if state, ok := manager.Snapshot(m.sessID); !ok || state.Process != "running" {
		t.Fatalf("raw-control child stopped: %+v", state)
	}
}

func TestTerminalNativeRawRecordingChild(t *testing.T) {
	if len(os.Args) != 5 || os.Args[3] != "console-tui-raw-input" {
		return
	}
	input := windows.Handle(os.Stdin.Fd())
	var before, after, output uint32
	if err := windows.GetConsoleMode(input, &before); err != nil {
		t.Fatal(err)
	}
	if err := windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &output); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetConsoleMode(input, windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		t.Fatal(err)
	}
	if err := windows.GetConsoleMode(input, &after); err != nil || after != windows.ENABLE_VIRTUAL_TERMINAL_INPUT {
		t.Fatalf("raw console mode=%d err=%v", after, err)
	}
	fmt.Printf("READY:%d:%d:%d", before, after, output)
	var data []byte
	for {
		var characters [64]uint16
		var count uint32
		if err := windows.ReadConsole(input, &characters[0], uint32(len(characters)), &count, nil); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatal("raw console returned no characters")
		}
		for i, character := range characters[:count] {
			if character == 30 {
				if uint32(i+1) != count {
					t.Fatal("characters followed the input completion delimiter")
				}
				if err := os.WriteFile(os.Args[4], data, 0600); err != nil {
					t.Fatal(err)
				}
				fmt.Print("RAW-INPUT-CAPTURED")
				for {
					if err := windows.ReadConsole(input, &characters[0], uint32(len(characters)), &count, nil); err != nil {
						t.Fatal(err)
					}
					if count == 0 {
						t.Fatal("live console input closed")
					}
				}
			}
			if character > 127 {
				t.Fatalf("unexpected non-ASCII input U+%04X", character)
			}
			data = append(data, byte(character))
		}
	}
}
