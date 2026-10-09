//go:build !windows

package tui

import (
	"context"
	"github.com/batuta-ai/console/internal/session"
	"testing"
)

func terminalRawInputFixture(t *testing.T) (map[string][]string, func(context.Context, *session.TerminalSubscription, *termLink, int) []byte) {
	t.Helper()
	return nil, func(ctx context.Context, sub *session.TerminalSubscription, _ *termLink, count int) []byte {
		var got []byte
		for len(got) < count {
			e, err := sub.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, e.Data...)
		}
		return got
	}
}
