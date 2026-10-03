package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestListSessionsSingleConnection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.SetMaxOpenConns(1)
	created := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	ended := created.Add(time.Minute)
	older := domain.Session{
		ID: "older", MissionID: "mission", WorkspaceID: "workspace", MemberID: "member",
		AccountID: "account", Adapter: "codex", Argv: []string{"codex", "argument with spaces", "日本語"},
		Process: "exited", Work: "interactive", Owner: "tui", OwnerClient: "client",
		PID: 123, Cols: 120, Rows: 50, BatutaInjected: true, SkillsVersion: "v1",
		CreatedAt: created, EndedAt: &ended,
	}
	newer := domain.Session{ID: "newer", Adapter: "claude", Process: "running", CreatedAt: created.Add(time.Second)}
	for _, sess := range []domain.Session{newer, older} {
		if err := s.PutSession(sess); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSession(sess.ID)
		if err != nil || !reflect.DeepEqual(got, sess) {
			t.Fatalf("fixture roundtrip: got %+v, want %+v, error %v", got, sess, err)
		}
	}
	type result struct {
		sessions []domain.Session
		err      error
	}
	done := make(chan result, 1)
	go func() {
		list, err := s.ListSessions()
		done <- result{list, err}
	}()
	// This watchdog diagnoses a leaked/nested connection, not query speed.
	watchdog := time.NewTimer(5 * time.Second)
	defer watchdog.Stop()
	select {
	case got := <-done:
		if got.err != nil || !reflect.DeepEqual(got.sessions, []domain.Session{newer, older}) {
			t.Fatalf("list values/order: %+v, error %v", got.sessions, got.err)
		}
	case <-watchdog.C:
		stats := s.db.Stats()
		// Closing the pool wakes a nested query waiting for its connection.
		closeErr := s.Close()
		<-done
		t.Fatalf("ListSessions blocked with one connection: in-use=%d, waiting=%d, close error=%v", stats.InUse, stats.WaitCount, closeErr)
	}
	if inUse := s.db.Stats().InUse; inUse != 0 {
		t.Fatalf("list retained %d connections", inUse)
	}
	if err := s.PutSessionWithEvent(newer, "session.updated", map[string]any{"id": newer.ID}); err != nil {
		t.Fatalf("write after list: %v", err)
	}
	execSQL(t, s.db, `UPDATE sessions SET pid='invalid' WHERE id='newer'`)
	if _, err := s.ListSessions(); err == nil {
		t.Fatal("invalid stored PID did not return a scan error")
	}
	if inUse := s.db.Stats().InUse; inUse != 0 {
		t.Fatalf("failed scan retained %d connections", inUse)
	}
	execSQL(t, s.db, `DELETE FROM sessions`)
	empty, err := s.ListSessions()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list contract: %#v, error %v", empty, err)
	}
	if inUse := s.db.Stats().InUse; inUse != 0 {
		t.Fatalf("empty list retained %d connections", inUse)
	}
}

func TestConcurrentCatalogReadsAndWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const writers, readers, iterations = 4, 4, 20
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < writers; i++ {
		if err := s.PutSession(domain.Session{ID: fmt.Sprint(i), CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	type expectedEvent struct{ kind, id, step string }
	successful := make(chan expectedEvent, writers*iterations*2)
	errors := make(chan error, writers+readers)
	start := make(chan struct{})
	var ready, workers sync.WaitGroup
	launch := func(work func() error) {
		ready.Add(1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			ready.Done()
			<-start
			if err := work(); err != nil {
				errors <- err
			}
		}()
	}
	for i := 0; i < writers; i++ {
		id := fmt.Sprint(i)
		launch(func() error {
			for step := 1; step <= iterations; step++ {
				payload := map[string]any{"id": id, "step": fmt.Sprint(step)}
				sess := domain.Session{ID: id, PID: step, Process: "running", CreatedAt: created, Argv: []string{id, fmt.Sprint(step)}}
				if err := s.PutSessionWithEvent(sess, "session.updated", payload); err != nil {
					return fmt.Errorf("atomic writer %s step %d: %w", id, step, err)
				}
				successful <- expectedEvent{"session.updated", id, fmt.Sprint(step)}
				if err := s.PutAccount(domain.Account{ID: id, Label: fmt.Sprint(step)}); err != nil {
					return fmt.Errorf("bare writer %s step %d: %w", id, step, err)
				}
				if _, err := s.AppendEvent("observed", payload); err != nil {
					return fmt.Errorf("event writer %s step %d: %w", id, step, err)
				}
				successful <- expectedEvent{"observed", id, fmt.Sprint(step)}
			}
			return nil
		})
	}
	for i := 0; i < readers; i++ {
		launch(func() error {
			for step := 0; step < iterations*3; step++ {
				sessions, err := s.ListSessions()
				if err != nil {
					return fmt.Errorf("list reader: %w", err)
				}
				if len(sessions) != writers {
					return fmt.Errorf("list lost sessions: %d", len(sessions))
				}
				events, err := s.EventsSince(0, writers*iterations*2+1)
				if err != nil {
					return fmt.Errorf("event reader: %w", err)
				}
				seen := make(map[expectedEvent]bool)
				for _, event := range events {
					seen[expectedEvent{event.Kind, fmt.Sprint(event.Payload["id"]), fmt.Sprint(event.Payload["step"])}] = true
				}
				// Events are read after the catalog snapshot: every observed committed
				// update must already have its event, even if later updates occurred.
				for _, sess := range sessions {
					if sess.PID > 0 && !seen[expectedEvent{"session.updated", sess.ID, fmt.Sprint(sess.PID)}] {
						return fmt.Errorf("session %s step %d visible without its event", sess.ID, sess.PID)
					}
				}
				if _, err := s.ListAccounts(); err != nil {
					return fmt.Errorf("account reader: %w", err)
				}
			}
			return nil
		})
	}
	ready.Wait()
	close(start)
	workers.Wait()
	close(errors)
	close(successful)
	for err := range errors {
		t.Error(err)
	}
	want := make(map[expectedEvent]int)
	for event := range successful {
		want[event]++
	}
	events, err := s.EventsSince(0, writers*iterations*2+1)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[expectedEvent]int)
	for i, event := range events {
		if event.Seq != int64(i+1) || len(event.Payload) != 2 {
			t.Errorf("unexpected event sequence/payload: %+v", event)
		}
		got[expectedEvent{event.Kind, fmt.Sprint(event.Payload["id"]), fmt.Sprint(event.Payload["step"])}]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events differ from successful writes: got %v, want %v", got, want)
	}
	if len(events) != writers*iterations*2 {
		t.Errorf("got %d events, want %d", len(events), writers*iterations*2)
	}
	for i := 0; i < writers; i++ {
		id := fmt.Sprint(i)
		sess, err := s.GetSession(id)
		if err != nil || sess.PID != iterations || !reflect.DeepEqual(sess.Argv, []string{id, fmt.Sprint(iterations)}) {
			t.Errorf("final session %s: %+v, error %v", id, sess, err)
		}
		account, err := s.GetAccount(id)
		if err != nil || account.Label != fmt.Sprint(iterations) {
			t.Errorf("final account %s: %+v, error %v", id, account, err)
		}
	}
	if inUse := s.db.Stats().InUse; inUse != 0 {
		t.Errorf("concurrent operations retained %d connections", inUse)
	}
}
