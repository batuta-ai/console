package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTerminalMaximumControlFloodCancellation(t *testing.T) {
	for name, sequence := range map[string]string{"LF": "\n", "IND": "\x1bD", "S": "\x1b[S", "T": "\x1b[T", "L": "\x1b[L", "M": "\x1b[M", "2026": "\x1b[?2026h\n"} {
		t.Run(name, func(t *testing.T) {
			var screen terminalScreen
			initial := ""
			if name == "LF" || name == "IND" || name == "2026" {
				initial = "\x1b[200;1H"
			}
			screen.feed(500, 200, initial)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, joined := make(chan struct{}), make(chan struct{})
			var operations time.Duration
			go func() {
				defer close(joined)
				begin := time.Now()
				for i := 0; i < 100; i++ {
					screen.feedTurn(ctx, 500, 200, sequence)
				}
				operations = time.Since(begin)
				close(started)
				screen.feedContext(ctx, 500, 200, strings.Repeat(sequence, 1<<18))
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("100 maximum-geometry operations did not complete")
			}
			begin := time.Now()
			cancel()
			select {
			case <-joined:
			case <-time.After(5 * time.Second):
				t.Fatal("control flood did not join after cancellation")
			}
			t.Logf("100 operations=%s cancel/join=%s", operations, time.Since(begin))
		})
	}
}
