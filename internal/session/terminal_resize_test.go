package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestTerminalResizeFailurePreservesGeometry(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-pty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := New(t.TempDir())
	m.live["s"] = &live{sess: domain.Session{ID: "s", OwnerClient: "owner", Cols: 80, Rows: 24}, pty: f}
	sub, err := m.SubscribeTerminal("s")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = sub.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Resize("s", "owner", 97, 31); err == nil {
		t.Fatal("regular file resize succeeded")
	}
	got, _ := m.Snapshot("s")
	if got.Cols != 80 || got.Rows != 24 {
		t.Fatalf("failed resize published false geometry: %dx%d", got.Cols, got.Rows)
	}
	l, _ := m.Live("s")
	l.mu.Lock()
	l.publishTerminal(TerminalEvent{Data: []byte("after-failure")})
	l.mu.Unlock()
	event, err := sub.Next(ctx)
	if err != nil || string(event.Data) != "after-failure" {
		t.Fatalf("failed resize broadcast geometry: %+v %v", event, err)
	}
}
