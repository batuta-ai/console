package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

func TestReferenceHTTPStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	svc := service.New(st, session.New(t.TempDir()), nil)
	svc.Home = t.TempDir()
	h := (&Server{Svc: svc, Token: "test-token"}).Handler()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	ws, err := svc.CreateWorkspace("fixture", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sq, err := svc.SaveSquad(domain.Squad{Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.CreateMission(ws.ID, "fixture", "", sq.ID)
	if err != nil {
		t.Fatal(err)
	}
	account, err := svc.CreateAccount("synthetic fixture", "codex")
	if err != nil {
		t.Fatal(err)
	}
	cli := t.TempDir()
	artifact := filepath.Join(cli, "launches")
	script := "#!/bin/sh\nprintf 'launch\\n' >> '" + artifact + "'\nwhile IFS= read -r line; do printf 'ack:%s\\n' \"$line\"; done\n"
	if err := os.WriteFile(filepath.Join(cli, "codex"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cli)
	control := post(t, h, "/v0/sessions", map[string]any{"mission_id": m.ID, "batuta": false, "owner": "web", "client": "test"}, 200)
	if member, ok := control["member_id"]; ok && member != "" {
		t.Fatalf("fictional generic member: %v", member)
	}
	id := control["id"].(string)
	ack := make(chan struct{}, 1)
	var mu sync.Mutex
	var output string
	unsubscribe, err := svc.Session.Subscribe(id, func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		output += string(b)
		if strings.Contains(output, "ack:ready") {
			select {
			case ack <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := svc.Session.Input(id, "test", []byte("ready\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ack:
	case <-time.After(5 * time.Second):
		t.Fatal("disposable CLI did not acknowledge")
	}
	before, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := svc.Store.LastEventSeq()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		body       any
		status     int
	}{
		{"missing mission", "/v0/sessions", map[string]any{"mission_id": "missing", "batuta": false}, 404},
		{"missing account", "/v0/sessions", map[string]any{"mission_id": m.ID, "account_id": "missing", "batuta": false}, 404},
		{"missing member", "/v0/sessions", map[string]any{"mission_id": m.ID, "member_id": "missing", "batuta": false}, 404},
		{"account adapter mismatch", "/v0/sessions", map[string]any{"mission_id": m.ID, "account_id": account.ID, "adapter": "claude", "batuta": false}, 400},
		{"unknown adapter", "/v0/sessions", map[string]any{"mission_id": m.ID, "adapter": "unknown", "batuta": false}, 400},
		{"empty injected snapshot", "/v0/sessions", map[string]any{"mission_id": m.ID}, 400},
		{"missing workspace", "/v0/missions", map[string]any{"workspace_id": "missing", "title": "fixture", "squad_id": sq.ID}, 404},
		{"missing squad", "/v0/missions", map[string]any{"workspace_id": ws.ID, "title": "fixture", "squad_id": "missing"}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) { post(t, h, tc.path, tc.body, tc.status) })
	}
	sessions, err := svc.Store.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions %v %v", sessions, err)
	}
	after, err := os.ReadFile(artifact)
	if err != nil || string(before) != string(after) {
		t.Fatalf("invalid HTTP input spawned child: %q %v", after, err)
	}
	afterSeq, err := svc.Store.LastEventSeq()
	if err != nil || seq != afterSeq {
		t.Fatalf("invalid HTTP input mutated events: %d %d %v", seq, afterSeq, err)
	}
	if _, err := svc.StopSession(id); err != nil {
		t.Fatal(err)
	}
	seq, err = svc.Store.LastEventSeq()
	if err != nil {
		t.Fatal(err)
	}
	missions, err := svc.Store.ListMissions("")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	post(t, h, "/v0/sessions", map[string]any{"mission_id": m.ID, "batuta": false}, 500)
	post(t, h, "/v0/missions", map[string]any{"workspace_id": ws.ID, "title": "fixture", "squad_id": sq.ID}, 500)
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	afterSeq, err = reopened.LastEventSeq()
	if err != nil || seq != afterSeq {
		t.Fatalf("closed catalog mutated events: %d %d %v", seq, afterSeq, err)
	}
	sessions, err = reopened.ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].Process != "exited" {
		t.Fatalf("closed catalog sessions: %v %v", sessions, err)
	}
	afterMissions, err := reopened.ListMissions("")
	if err != nil || len(afterMissions) != len(missions) {
		t.Fatalf("closed catalog missions: %v %v", afterMissions, err)
	}
	after, err = os.ReadFile(artifact)
	if err != nil || string(before) != string(after) {
		t.Fatalf("closed catalog spawned child: %q %v", after, err)
	}
}
