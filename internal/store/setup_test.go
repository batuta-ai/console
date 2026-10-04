package store

import (
	"path/filepath"
	"testing"
)

func TestExactSessionStopEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, payload := range []string{`{"id":"session-prefix"}`, `{"id":12}`, `{"session":"session"}`, `null`, `broken`, `{"id":null}`} {
		if _, err := s.db.Exec(`INSERT INTO events(at,kind,payload) VALUES('', 'session.stopped', ?)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendEvent("session.exited", map[string]any{"id": "session"}); err != nil {
		t.Fatal(err)
	}
	if found, err := s.HasSessionStopRequest("session"); err != nil || found {
		t.Fatalf("false match: %v %v", found, err)
	}
	if _, err := s.AppendEvent("session.stopped", map[string]any{"id": "session"}); err != nil {
		t.Fatal(err)
	}
	for range 1200 {
		if _, err := s.AppendEvent("unrelated", map[string]any{"id": "other"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if found, err := s.HasSessionStopRequest("session"); err != nil || !found {
		t.Fatalf("old exact event lost: %v %v", found, err)
	}
	if found, err := s.HasSessionStopRequest("sess"); err != nil || found {
		t.Fatalf("prefix match: %v %v", found, err)
	}
	if _, err := s.db.Exec("DROP TABLE events"); err != nil {
		t.Fatal(err)
	}
	if found, err := s.HasSessionStopRequest("session"); err == nil || found {
		t.Fatalf("query failure masked: %v %v", found, err)
	}
}
