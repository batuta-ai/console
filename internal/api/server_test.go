package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/plugins/limits"
	"github.com/coder/websocket"
)

func TestPickWorkspaceRoot(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := service.New(st, session.New(t.TempDir()), limits.New(nil))
	dir := t.TempDir()
	h := (&Server{
		Svc:   svc,
		Token: "test-token",
		PickDirectory: func(ctx context.Context) (string, bool, error) {
			return dir, false, nil
		},
	}).Handler()
	got := post(t, h, "/v0/workspaces/pick-root", map[string]any{}, 200)
	if got["path"] != dir {
		t.Fatalf("%v", got)
	}

	h = (&Server{
		Svc:   svc,
		Token: "test-token",
		PickDirectory: func(ctx context.Context) (string, bool, error) {
			return "", true, nil
		},
	}).Handler()
	got = post(t, h, "/v0/workspaces/pick-root", map[string]any{}, 200)
	if got["canceled"] != true {
		t.Fatalf("cancel %v", got)
	}
}

func TestCatalogDoesNotStartSession(t *testing.T) {
	h, _ := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	acct := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "purpose": "p", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": acct["id"], "maestro": true}}}, 200)
	if sq["draft"] == true {
		t.Fatalf("ready squad marked draft: %v", sq)
	}
	val := post(t, h, "/v0/squads/"+sq["id"].(string)+"/validate", map[string]any{}, 200)
	v := val["validation"].(map[string]any)
	if v["ready"] != true {
		t.Fatalf("validate %+v", val)
	}
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	if msn["title"] != "Checkout" {
		t.Fatal(msn)
	}
	sessions := get(t, h, "/v0/sessions")
	if len(sessions.([]any)) != 0 {
		t.Fatalf("save/validate/create mission must not start a session: %v", sessions)
	}
}

func TestValidateDraftDoesNotSave(t *testing.T) {
	h, _ := testServer(t)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": "a1", "maestro": true}}}, 200)
	before := sq["version"]
	val := post(t, h, "/v0/squads/"+sq["id"].(string)+"/validate", map[string]any{
		"name": "Web", "members": []map[string]any{{"role": "Builder", "cli": "codex", "account_id": "a1"}},
	}, 200)
	v := val["validation"].(map[string]any)
	if v["ready"] == true || v["tone"] != "warning" {
		t.Fatalf("draft without maestro should warn: %+v", val)
	}
	again := get(t, h, "/v0/squads").([]any)[0].(map[string]any)
	if again["version"] != before {
		t.Fatalf("validate must not bump version: %v -> %v", before, again["version"])
	}
}

func TestSessionReconnect(t *testing.T) {
	h, svc := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	acct := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": acct["id"], "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	sess := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "argv": []string{"/bin/sleep", "10"}, "owner": "tui", "client": "c1"}, 200)
	id := sess["id"].(string)
	if sess["process"] != "running" {
		t.Fatalf("%v", sess)
	}
	live, ok := svc.Session.Snapshot(id)
	if !ok {
		t.Fatal("live session missing")
	}
	unsub, err := svc.Session.Subscribe(id, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	unsub()
	again, ok := svc.Session.Snapshot(id)
	if !ok || again.PID != live.PID {
		t.Fatal("dropping a client must not kill the process")
	}
	post(t, h, "/v0/sessions/"+id+"/stop", map[string]any{}, 200)
}

func TestReconcileMarksUnknownWhenPTYGone(t *testing.T) {
	h, svc := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": "a1", "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	lost := domain.Session{
		ID: "s_lost", MissionID: msn["id"].(string), WorkspaceID: ws["id"].(string),
		Adapter: "codex", Argv: []string{"/bin/sleep"}, Process: "running", Work: "interactive",
		Owner: "none", CreatedAt: domain.Now(),
	}
	if err := svc.Store.PutSession(lost); err != nil {
		t.Fatal(err)
	}
	list := get(t, h, "/v0/sessions").([]any)
	if len(list) != 1 {
		t.Fatalf("%v", list)
	}
	got := list[0].(map[string]any)
	if got["process"] != "unknown" {
		t.Fatalf("expected catalog-only unknown, got %v", got["process"])
	}
}

func TestTwoCodexAccountsIndependent(t *testing.T) {
	h, svc := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	a1 := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	a2 := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Home", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": a1["id"], "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	dir := t.TempDir()
	f1 := filepath.Join(dir, "home1")
	f2 := filepath.Join(dir, "home2")
	s1 := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a1["id"], "argv": []string{"/bin/sh", "-c", "printf %s \"$HOME\" > " + f1 + "; sleep 4"}, "owner": "tui", "client": "c1"}, 200)
	s2 := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a2["id"], "argv": []string{"/bin/sh", "-c", "printf %s \"$HOME\" > " + f2 + "; sleep 4"}, "owner": "web", "client": "c2"}, 200)
	if s1["process"] != "running" || s2["process"] != "running" {
		t.Fatalf("both accounts must run: %v %v", s1, s2)
	}
	if s1["account_id"] == s2["account_id"] {
		t.Fatal("sessions must keep distinct accounts")
	}
	deadline := time.Now().Add(2 * time.Second)
	var h1, h2 []byte
	for time.Now().Before(deadline) {
		h1, _ = os.ReadFile(f1)
		h2, _ = os.ReadFile(f2)
		if len(h1) > 0 && len(h2) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if string(h1) == "" || string(h1) == string(h2) || !strings.Contains(string(h1), a1["id"].(string)) || !strings.Contains(string(h2), a2["id"].(string)) {
		t.Fatalf("isolated HOME: %q vs %q (svc home %s)", h1, h2, svc.Home)
	}
	post(t, h, "/v0/sessions/"+s1["id"].(string)+"/stop", map[string]any{}, 200)
	post(t, h, "/v0/sessions/"+s2["id"].(string)+"/stop", map[string]any{}, 200)
}

func TestAccountSessionCap(t *testing.T) {
	h, _ := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	a := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": a1id(a), "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	var ids []string
	for i := 0; i < 3; i++ {
		s := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a["id"], "argv": []string{"/bin/sleep", "8"}, "owner": "tui", "client": "c"}, 200)
		ids = append(ids, s["id"].(string))
	}
	req, rec := postRaw(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a["id"], "argv": []string{"/bin/sleep", "1"}, "owner": "tui", "client": "c"})
	if rec != 400 {
		t.Fatalf("4th session on cap-3 account must fail, got %d %s", rec, req)
	}
	for _, id := range ids {
		post(t, h, "/v0/sessions/"+id+"/stop", map[string]any{}, 200)
	}
}

func TestConnectCreatesIsolateDir(t *testing.T) {
	h, svc := testServer(t)
	a := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	got := post(t, h, "/v0/accounts/"+a["id"].(string)+"/connect", map[string]any{}, 200)
	ref, _ := got["vault_ref"].(string)
	if ref == "" || !strings.Contains(ref, a["id"].(string)) || !strings.HasPrefix(ref, svc.Home) {
		t.Fatalf("isolate dir %q home %s account %v", ref, svc.Home, got)
	}
	st, err := os.Stat(ref)
	if err != nil || !st.IsDir() {
		t.Fatalf("isolate dir missing: %s %v", ref, err)
	}
	if got["connected"] != true {
		t.Fatalf("catalog must mark connected: %v", got)
	}
}

func TestEventsLiveAfterSnapshot(t *testing.T) {
	h, svc := testServer(t)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v0/events?token=test-token"
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{ts.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]any
	if err := json.Unmarshal(data, &snap); err != nil || snap["type"] != "snapshot" {
		t.Fatalf("snapshot: %s", data)
	}
	if _, err := svc.Store.AppendEvent("probe.live", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	_, data, err = c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil || msg["type"] != "event" {
		t.Fatalf("live event: %s", data)
	}
}

func a1id(a map[string]any) string { return a["id"].(string) }

func postRaw(t *testing.T, h http.Handler, path string, body any) (string, int) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.String(), rec.Code
}

func TestDuplicateWorkspaceKeepsID(t *testing.T) {
	h, _ := testServer(t)
	root := t.TempDir()
	w1 := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": root}, 200)
	w2 := post(t, h, "/v0/workspaces", map[string]string{"name": "shop-again", "root": root}, 200)
	if w1["id"] != w2["id"] {
		t.Fatalf("duplicate root replaced identity: %v vs %v", w1, w2)
	}
}

func TestUnknownAccountRejected(t *testing.T) {
	h, _ := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	acct := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": acct["id"], "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	_, code := postRaw(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": "nope", "argv": []string{"/bin/true"}})
	if code != 400 {
		t.Fatalf("unknown account must 400, got %d", code)
	}
}

func TestNaturalExitIsExited(t *testing.T) {
	h, _ := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	acct := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": acct["id"], "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "argv": []string{"/usr/bin/true"}, "owner": "tui", "client": "c1"}, 200)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list := get(t, h, "/v0/sessions").([]any)
		if len(list) == 1 {
			got := list[0].(map[string]any)
			if got["process"] == "exited" {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("natural exit stayed %v", get(t, h, "/v0/sessions"))
}

func TestAnswerRejectsUnknownWorkspace(t *testing.T) {
	h, _ := testServer(t)
	_, code := postRaw(t, h, "/v0/watch/answer", map[string]any{"workspace": "/tmp", "task": "t1", "text": "ok"})
	if code != 400 {
		t.Fatalf("got %d", code)
	}
}

func TestSkillsCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "batuta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "batuta", "SKILL.md"), []byte("#\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATUTA_SKILLS", root)
	h, svc := testServer(t)
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	raw := get(t, h, "/v0/skills").(map[string]any)
	if raw["root"] != root {
		t.Fatalf("root %v", raw["root"])
	}
	skills := raw["skills"].([]any)
	if len(skills) < 11 {
		t.Fatalf("official skills missing: %d", len(skills))
	}
	list := get(t, h, "/v0/squads").([]any)
	if len(list) != 4 {
		t.Fatalf("seeded squads %d", len(list))
	}
	origin := list[0].(map[string]any)["origin"]
	if origin != "batuta" {
		t.Fatalf("origin %v", origin)
	}
}

func testServer(t *testing.T) (http.Handler, *service.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := service.New(st, session.New(t.TempDir()), limits.New(nil))
	svc.Home = t.TempDir()
	return (&Server{Svc: svc, Token: "test-token"}).Handler(), svc
}

func post(t *testing.T, h http.Handler, path string, body any, code int) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != code {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func get(t *testing.T, h http.Handler, path string) any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	var out any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}
