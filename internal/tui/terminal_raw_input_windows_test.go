package tui

import (
	"bytes"
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func terminalRawInputFixture(t *testing.T) (map[string][]string, func(context.Context, *session.TerminalSubscription, *termLink, int) []byte) {
	t.Helper()
	record := filepath.Join(testfixture.PrivateHome(t), "actual-terminal-input.bin")
	quoted := "'" + strings.ReplaceAll(record, "'", "'\\''") + "'"
	script := "stty raw -echo; printf READY; head -c 6 > " + quoted + "; printf RAW-INPUT-CAPTURED; exec /bin/cat"
	commands := map[string][]string{"z-selected": testfixture.Argv(t, []string{"/bin/sh", "-c", script})}
	// ConPTY output is a terminal presentation; observe raw input at the child.
	return commands, func(ctx context.Context, sub *session.TerminalSubscription, link *termLink, _ int) []byte {
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
