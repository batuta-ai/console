package tui

import (
	"context"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terminalRawInputFixture(t *testing.T) (map[string][]string, func(context.Context, *session.TerminalSubscription, int) []byte) {
	t.Helper()
	record := filepath.Join(testfixture.PrivateHome(t), "actual-terminal-input.bin")
	// ConPTY output is a terminal presentation; observe raw input at the child.
	return nil, func(ctx context.Context, sub *session.TerminalSubscription, _ int) []byte {
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
