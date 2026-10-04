package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestEventReadCorruption(t *testing.T) {
	for _, statement := range []string{"UPDATE events SET at='broken'", "UPDATE events SET payload='{broken'", "UPDATE events SET payload='[]'"} {
		t.Run(statement, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.AppendEvent("test", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			for _, read := range []func() error{func() error { _, err := s.EventsSince(0, 200); return err }, func() error { _, err := s.EventsRecent(200); return err }} {
				if err := read(); err == nil {
					t.Error("corrupt event became successful read")
				}
			}
		})
	}
}

func TestEventReadCancellationAndLimits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 205; i++ {
		if _, err := s.AppendEvent("test", nil); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.EventsSince(0, 0)
	if err != nil || len(events) != 200 || events[0].Seq != 1 || events[199].Seq != 200 {
		t.Fatalf("default since limit: %v %v", events, err)
	}
	events, err = s.EventsRecent(0)
	if err != nil || len(events) != 100 || events[0].Seq != 106 || events[99].Seq != 205 {
		t.Fatalf("default recent ordering/limit: %v %v", events, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.EventsSinceContext(ctx, 0, 200); !errors.Is(err, context.Canceled) {
		t.Errorf("since cancellation: %v", err)
	}
	if _, err := s.EventsRecentContext(ctx, 200); !errors.Is(err, context.Canceled) {
		t.Errorf("recent cancellation: %v", err)
	}
}
