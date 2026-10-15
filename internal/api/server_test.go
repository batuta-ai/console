package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/console/plugins/limits"
	"github.com/coder/websocket"
)

func TestCapabilitiesLang(t *testing.T) {
	t.Setenv("BATUTA_LANG", "pt-BR")
	h, _ := testServer(t)
	got := get(t, h, "/v0/capabilities").(map[string]any)
	if got["lang"] != "pt" {
		t.Fatalf("lang %v", got["lang"])
	}
	t.Setenv("BATUTA_LANG", "en")
	got = get(t, h, "/v0/capabilities").(map[string]any)
	if got["lang"] != "en" {
		t.Fatalf("lang %v", got["lang"])
	}
	raw, _ := got["adapters"].([]any)
	found := false
	for _, a := range raw {
		if a == "cursor-agent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("adapters %v", raw)
	}
}

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
	if sq["draft"] != false {
		t.Fatalf("structurally complete squad marked draft: %v", sq)
	}
	val := post(t, h, "/v0/squads/"+sq["id"].(string)+"/validate", map[string]any{}, 200)
	v := val["validation"].(map[string]any)
	if v["ready"] != false {
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
	sess := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "argv": testfixture.Argv(t, []string{"/bin/sleep", "10"}), "owner": "tui", "client": "c1"}, 200)
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
	dir := filepath.Join(t.TempDir(), "account output's")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.ToSlash(filepath.Join(dir, "home1"))
	f2 := filepath.ToSlash(filepath.Join(dir, "home2"))
	s1 := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a1["id"], "argv": testfixture.Argv(t, []string{"/bin/sh", "-c", "printf %s \"$HOME\" > " + "'" + strings.ReplaceAll(f1, "'", "'\"'\"'") + "'; sleep 4"}), "owner": "tui", "client": "c1"}, 200)
	s2 := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a2["id"], "argv": testfixture.Argv(t, []string{"/bin/sh", "-c", "printf %s \"$HOME\" > " + "'" + strings.ReplaceAll(f2, "'", "'\"'\"'") + "'; sleep 4"}), "owner": "web", "client": "c2"}, 200)
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
		s := post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a["id"], "argv": testfixture.Argv(t, []string{"/bin/sleep", "8"}), "owner": "tui", "client": "c"}, 200)
		ids = append(ids, s["id"].(string))
	}
	req, rec := postRaw(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "account_id": a["id"], "argv": testfixture.Argv(t, []string{"/bin/sleep", "1"}), "owner": "tui", "client": "c"})
	if rec != 400 {
		t.Fatalf("4th session on cap-3 account must fail, got %d %s", rec, req)
	}
	for _, id := range ids {
		post(t, h, "/v0/sessions/"+id+"/stop", map[string]any{}, 200)
	}
}

func TestRenameAccountLabel(t *testing.T) {
	h, _ := testServer(t)
	a := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	body, _ := json.Marshal(map[string]string{"label": "trabalho"})
	req := httptest.NewRequest(http.MethodPatch, "/v0/accounts/"+a["id"].(string), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["label"] != "trabalho" || got["adapter"] != "codex" {
		t.Fatalf("%v", got)
	}
	empty, _ := json.Marshal(map[string]string{"label": "  "})
	bad := httptest.NewRequest(http.MethodPatch, "/v0/accounts/"+a["id"].(string), bytes.NewReader(empty))
	bad.Header.Set("Authorization", "Bearer test-token")
	bad.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != 400 {
		t.Fatalf("empty label %d", rec.Code)
	}
	missing := httptest.NewRequest(http.MethodPatch, "/v0/accounts/acct_missing", bytes.NewReader(body))
	missing.Header.Set("Authorization", "Bearer test-token")
	missing.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, missing)
	if rec.Code != 404 {
		t.Fatalf("missing %d", rec.Code)
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
	if code != 404 {
		t.Fatalf("unknown account must 404, got %d", code)
	}
}

func TestNaturalExitIsExited(t *testing.T) {
	h, _ := testServer(t)
	ws := post(t, h, "/v0/workspaces", map[string]string{"name": "shop", "root": t.TempDir()}, 200)
	acct := post(t, h, "/v0/accounts", map[string]string{"label": "Codex Work", "adapter": "codex"}, 200)
	sq := post(t, h, "/v0/squads", map[string]any{"name": "Web", "members": []map[string]any{{"role": "Maestro", "cli": "codex", "account_id": acct["id"], "maestro": true}}}, 200)
	msn := post(t, h, "/v0/missions", map[string]any{"workspace_id": ws["id"], "title": "Checkout", "squad_id": sq["id"]}, 200)
	post(t, h, "/v0/sessions", map[string]any{"mission_id": msn["id"], "argv": testfixture.Argv(t, []string{"/usr/bin/true"}), "owner": "tui", "client": "c1"}, 200)
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
	if err := os.MkdirAll(filepath.Join(root, "batuta", "adapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "batuta", "SKILL.md"), []byte("#\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsRoot, err := filepath.EvalSymlinks(filepath.Join(root, "batuta"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATUTA_SKILLS", root)
	h, svc := testServer(t)
	if err := svc.EnsureBatutaCatalog(); err != nil {
		t.Fatal(err)
	}
	raw := get(t, h, "/v0/skills").(map[string]any)
	if raw["root"] != skillsRoot {
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
	svc.Home = testfixture.PrivateHome(t)
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

func TestSessionPersistenceBoundaries(t *testing.T) {
	for _, mode := range []string{"control-event", "control-write", "list-write", "control-success"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			svc := service.New(st, session.New(t.TempDir()), nil)
			h := (&Server{Svc: svc, Token: "test-token"}).Handler()
			sess := domain.Session{ID: "s", Process: "running", Owner: "old", Argv: []string{"/bin/cat"}}
			if mode != "list-write" {
				sess.Argv = testfixture.Argv(t, sess.Argv)
				done := make(chan struct{})
				onExit := svc.Session.OnExit
				svc.Session.OnExit = func(s domain.Session) { onExit(s); close(done) }
				sess, err = svc.Session.Start(sess, nil, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					svc.Session.Stop(sess.ID)
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("process did not exit")
					}
				}()
			}
			if err := st.PutSession(sess); err != nil {
				t.Fatal(err)
			}
			table := "sessions"
			if mode == "control-event" {
				table = "events"
			}
			if mode != "control-success" {
				if _, err := db.Exec("CREATE TRIGGER reject_write BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'injected failure'); END"); err != nil {
					t.Fatal(err)
				}
			}
			pathURL, method, body := "/v0/sessions/s/control", http.MethodPost, `{"surface":"web","client":"new"}`
			if mode == "list-write" {
				pathURL, method, body = "/v0/sessions", http.MethodGet, ""
			}
			req := httptest.NewRequest(method, pathURL, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if mode == "control-success" {
				if rec.Code != 200 {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
				events, err := st.EventsSince(0, 100)
				if err != nil || len(events) != 1 || events[0].Kind != "session.control" || !reflect.DeepEqual(events[0].Payload, map[string]any{"id": "s", "owner": "web"}) {
					t.Fatalf("%v %v", events, err)
				}
				got, err := st.GetSession("s")
				if err != nil || got.Owner != "web" || got.OwnerClient != "new" {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			if rec.Code != 500 || !strings.Contains(rec.Body.String(), "injected failure") {
				t.Errorf("persistence error not returned: %d %s", rec.Code, rec.Body.String())
			}
			got, err := st.GetSession("s")
			if err != nil || got.Owner != "old" || got.Process != "running" {
				t.Errorf("catalog changed: %+v %v", got, err)
			}
			events, err := st.EventsSince(0, 100)
			if err != nil || len(events) != 0 {
				t.Errorf("event escaped transaction: %+v %v", events, err)
			}
		})
	}
}

// The same typed client crosses the authenticated handler without a listener or provider.
type namedClientHandlerTransport struct{ handler http.Handler }

func (tr namedClientHandlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	tr.handler.ServeHTTP(rec, r)
	return rec.Result(), nil
}
func TestNamedClientCatalogHandler(t *testing.T) {
	h, _ := testServer(t)
	c := client.New("http://fixture", "test-token")
	c.HTTP.Transport = namedClientHandlerTransport{h}
	ctx := context.Background()
	ws, err := c.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "selected", Root: t.TempDir()})
	if err != nil || ws.ID == "" {
		t.Fatalf("workspace %+v %v", ws, err)
	}
	q, err := c.SaveSquad(ctx, domain.Squad{Name: "local draft"})
	if err != nil || q.ID == "" {
		t.Fatalf("squad %+v %v", q, err)
	}
	val, err := c.ValidateSquad(ctx, q.ID, &domain.Squad{ID: q.ID, Name: "changed local draft"})
	if err != nil || val.Squad.ID != q.ID || val.Squad.Name != "changed local draft" {
		t.Fatalf("draft %+v %v", val, err)
	}
	squads, err := c.ListSquads(ctx)
	if err != nil || len(squads) != 1 || squads[0].Name != "local draft" {
		t.Fatalf("validation saved draft: %+v %v", squads, err)
	}
	m, err := c.CreateMission(ctx, client.CreateMissionRequest{WorkspaceID: ws.ID, Title: "exact mission", Brief: "exact brief", SquadID: q.ID})
	if err != nil || m.ID == "" || m.WorkspaceID != ws.ID {
		t.Fatalf("mission %+v %v", m, err)
	}
	missions, err := c.ListMissions(ctx, ws.ID)
	if err != nil || len(missions) != 1 || missions[0].ID != m.ID {
		t.Fatalf("missions %+v %v", missions, err)
	}
	watch, err := c.MissionWatch(ctx, m.ID, ws.ID, "")
	if err != nil || watch.MissionID != m.ID || watch.WorkspaceID != ws.ID || watch.Message != "mission_unassigned" {
		t.Fatalf("watch %+v %v", watch, err)
	}
	caps, err := c.Capabilities(ctx)
	if err != nil || caps.API != "v0" || caps.Matrix.Qualification.Status == "" || caps.Runtime.GoVersion == "" {
		t.Fatalf("capabilities %+v %v", caps, err)
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("catalog unexpectedly dispatched: %+v %v", sessions, err)
	}
	c.Token = "wrong"
	_, err = c.CreateMission(ctx, client.CreateMissionRequest{WorkspaceID: ws.ID})
	var failure *client.OperationError
	if !errors.As(err, &failure) || failure.Status != 401 || failure.Code != "unauthorized" || failure.Effect != client.EffectRejected {
		t.Fatalf("auth %v", err)
	}
}

func TestNamedClientReadFactsAcrossAuthenticatedHTTP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "batuta", "adapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "batuta", "SKILL.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsRoot, err := filepath.EvalSymlinks(filepath.Join(root, "batuta"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BATUTA_SKILLS", root)
	h, svc := testServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	c := client.New(ts.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	skills, err := c.Skills(ctx)
	if err != nil || skills.Root != skillsRoot || !skills.InjectDefault {
		t.Fatalf("skills %+v %v", skills, err)
	}
	found := false
	for _, skill := range skills.Skills {
		if skill.ID == "batuta" {
			found = true
			if !skill.Installed || skill.Origin != "batuta" || skill.Path != skillsRoot {
				t.Fatalf("installed skill %+v", skill)
			}
		}
	}
	if !found {
		t.Fatal("selected installed skill missing")
	}
	a := domain.Account{ID: "account-read-facts", Label: "selected account", Adapter: "codex"}
	if err := svc.Store.PutAccount(a); err != nil {
		t.Fatal(err)
	}
	quota, err := c.Limits(ctx)
	if err != nil || len(quota) != 1 {
		t.Fatalf("limits %+v %v", quota, err)
	}
	q := quota[0]
	if q.AccountID != a.ID || q.Label != a.Label || q.Window != "unknown" || q.Source != domain.SourceUnavailable || q.Origin != "not_qualified" || q.Percent != nil || q.ObservedAt != nil || q.ResetAt != nil || q.LastAttempt.IsZero() {
		t.Fatalf("unknown quota facts changed: %+v", q)
	}
	ws, err := c.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "selected", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	watch, err := c.WorkspaceWatch(ctx, ws.ID, "")
	if err != nil || watch.WorkspaceID != ws.ID || watch.Workspace != ws.Root || watch.Scope != "workspace" || watch.Message != "delivery_unassigned" || watch.Available || watch.Source != domain.SourceUnavailable {
		t.Fatalf("workspace watch %+v %v", watch, err)
	}
	event, err := svc.Store.AppendEvent("selected.read-facts", map[string]any{"account_id": a.ID, "workspace_id": ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	activity, err := c.Activity(ctx)
	if err != nil || len(activity) != 2 {
		t.Fatalf("activity %+v %v", activity, err)
	}
	if activity[0].Kind != "workspace.created" || activity[0].Seq != event.Seq-1 || activity[0].Payload["id"] != ws.ID {
		t.Fatalf("chronological activity changed: %+v", activity)
	}
	got := activity[1]
	if got.Seq != event.Seq || got.Kind != event.Kind || !got.At.Equal(event.At) || !reflect.DeepEqual(got.Payload, event.Payload) {
		t.Fatalf("activity identity changed: %+v want %+v", got, event)
	}
	for _, read := range []func() error{
		func() error { _, err := c.Skills(ctx); return err },
		func() error { _, err := c.Limits(ctx); return err },
		func() error { _, err := c.Activity(ctx); return err },
		func() error { _, err := c.WorkspaceWatch(ctx, ws.ID, ""); return err },
	} {
		c.Token = "wrong"
		var failure *client.OperationError
		if err := read(); !errors.As(err, &failure) || failure.Status != 401 || failure.Code != "unauthorized" || failure.Effect != client.EffectReadOnly {
			t.Fatalf("read authentication %v", err)
		}
	}
}

func TestNamedClientBindingAndRecoveryAcrossAuthenticatedHTTP(t *testing.T) {
	h, svc := testServer(t)
	ts := httptest.NewServer(h)
	defer ts.Close()
	c := client.New(ts.URL, "test-token")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, err := c.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "selected", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	path := bindingFixture(t, ws.Root, "selected", "selected-run")
	squad, err := c.SaveSquad(ctx, domain.Squad{Name: "selected squad"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.CreateMission(ctx, client.CreateMissionRequest{WorkspaceID: ws.ID, Title: "selected mission", Brief: "exact brief", SquadID: squad.ID})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := c.BindMission(ctx, m.ID, client.MissionBindingRequest{WorkspaceID: ws.ID, PlanPath: path, DeliveryID: "selected-run"})
	if err != nil || bound.ID != m.ID || bound.WorkspaceID != ws.ID || bound.Binding == nil || bound.Binding.WorkspaceID != ws.ID || bound.Binding.PlanPath != path || bound.Binding.DeliveryID != "selected-run" || bound.Binding.OpenedDigest == "" {
		t.Fatalf("binding %+v %v", bound, err)
	}
	record := domain.Session{ID: "selected-recovery", MissionID: m.ID, MemberID: "selected-member", Process: "unknown", RecoveryReason: "process_missing", Owner: "none"}
	if err := svc.Store.PutSession(record); err != nil {
		t.Fatal(err)
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("recoverable catalog %+v %v", sessions, err)
	}
	got := sessions[0]
	if got.ID != record.ID || got.MissionID != m.ID || got.MemberID != record.MemberID || got.Process != record.Process || got.RecoveryReason != record.RecoveryReason || got.Owner != "none" || got.OwnerClient != "" {
		t.Fatalf("recoverable identity changed: %+v", got)
	}
}

func TestNamedClientEventsAuthenticatedCursor(t *testing.T) {
	for _, mode := range []string{"zero", "exclusive", "future"} {
		t.Run(mode, func(t *testing.T) {
			h, svc := testServer(t)
			first, err := svc.Store.AppendEvent("selected.first", map[string]any{"mission_id": "exact"})
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.Store.AppendEvent("selected.second", map[string]any{"mission_id": "exact"})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(h)
			defer server.Close()
			c := client.New(server.URL, "test-token")
			cursor := int64(0)
			if mode == "exclusive" {
				cursor = first.Seq
			}
			if mode == "future" {
				cursor = second.Seq + 100
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := c.DialEvents(ctx, &cursor)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			env, err := stream.Read(ctx)
			if err != nil || env.Type != "snapshot" {
				t.Fatalf("snapshot %+v %v", env, err)
			}
			want := 2
			if mode == "exclusive" {
				want = 1
			}
			if mode == "future" {
				want = 0
			}
			if len(env.Events) != want {
				t.Fatalf("cursor reset/replayed %+v", env)
			}
			for _, ev := range env.Events {
				if ev.Seq <= cursor || ev.At.IsZero() || ev.Payload["mission_id"] != "exact" {
					t.Fatalf("event %+v", ev)
				}
			}
			if mode != "future" {
				live, err := svc.Store.AppendEvent("selected.live", map[string]any{"mission_id": "exact"})
				if err != nil {
					t.Fatal(err)
				}
				env, err = stream.Read(ctx)
				if err != nil || env.Type != "event" || env.Event == nil || env.Event.Seq != live.Seq {
					t.Fatalf("live %+v %v", env, err)
				}
			}
			c.Token = "wrong"
			_, err = c.DialEvents(ctx, &cursor)
			var failure *client.OperationError
			if !errors.As(err, &failure) || failure.Status != 401 || failure.Code != "unauthorized" || failure.Message == "" {
				t.Fatalf("handshake %v", err)
			}
		})
	}
}
