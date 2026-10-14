package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func bindingFixture(t *testing.T, root, slug, delivery string) string {
	t.Helper()
	path := routing.PlanPath(slug)
	body := []byte("# Plan — " + slug + "\n**Status:** approved\n\n- [ ] 1. " + slug + " task — data/high\n  Scope: internal/**\n  Accept: isolated evidence\n")
	if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := routing.ParsePlan(slug, body)
	if err != nil {
		t.Fatal(err)
	}
	if delivery != "" {
		js, err := journal.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		detail, _ := json.Marshal(map[string]any{"slug": slug, "workspace": canonical, "plan_path": path, "plan_digest": plan.Set.Digest, "tasks": []map[string]any{{"task_id": "task_1", "title": slug + " evidence"}}})
		if _, err := js.Append(delivery, journal.Record{Kind: loop.KindOpened, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Detail: detail, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestMissionBindingHTTP(t *testing.T) {
	h, svc := testServer(t)
	root := t.TempDir()
	path := bindingFixture(t, root, "alpha", "alpha-run")
	bindingFixture(t, root, "beta", "beta-run")
	ws := domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}
	if err := svc.Store.PutWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m1", "m2", "unassigned"} {
		if err := svc.Store.PutMission(domain.Mission{ID: id, WorkspaceID: ws.ID, Title: id, CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("legacy has no automatic journal", func(t *testing.T) {
		view := get(t, h, "/v0/watch?workspace=w").(map[string]any)
		if view["available"] != false || view["delivery"] != nil {
			t.Fatalf("adopted nearby delivery: %v", view)
		}
	})
	t.Run("unassigned mission ignores workspace plans", func(t *testing.T) {
		view := get(t, h, "/v0/watch?mission=unassigned&workspace=w").(map[string]any)
		if view["available"] != false || view["delivery"] != nil || view["plans"] != nil {
			t.Fatalf("cross-mission fallback: %v", view)
		}
	})
	t.Run("explicit authenticated binding", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/v0/missions/m1/binding", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("unauthenticated status %d", rec.Code)
		}
		bindingRequest(t, h, "m1", map[string]any{"workspace_id": "w", "plan_path": path, "delivery_id": "alpha-run"}, 200)
		view := get(t, h, "/v0/missions/m1/watch").(map[string]any)
		if view["delivery"] != "alpha-run" || view["mission_id"] != "m1" {
			t.Fatalf("wrong identity: %v", view)
		}
		plans, ok := view["plans"].([]any)
		if !ok || len(plans) != 1 || plans[0].(map[string]any)["name"] != "alpha" {
			t.Fatalf("wrong plans: %v", view)
		}
	})
}

func bindingRequest(t *testing.T, h http.Handler, mission string, body any, status int) map[string]any {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/v0/missions/"+mission+"/binding", strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("binding status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestMissionBindingIsolationAndReadOnly(t *testing.T) {
	h, svc := testServer(t)
	roots := []string{t.TempDir(), t.TempDir()}
	for i, root := range roots {
		wid := fmt.Sprintf("w%d", i)
		if err := svc.Store.PutWorkspace(domain.Workspace{ID: wid, Root: root, CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
		for _, slug := range []string{"alpha", "beta"} {
			id := wid + slug
			if err := svc.Store.PutMission(domain.Mission{ID: id, WorkspaceID: wid, CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			path := bindingFixture(t, root, slug, slug+"-run")
			js, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			state := routing.GraphTaskIntegrated
			if slug == "beta" {
				state = routing.GraphTaskPending
			}
			graph, _ := json.Marshal(routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: state, IntegratedCommitSHA: id + "-commit"}}, Waves: []routing.DeliveryWave{{Number: 1, TaskIDs: []string{"task_1"}}}})
			if _, err := js.Append(slug+"-run", journal.Record{Kind: loop.KindSettled, Graph: graph, Detail: json.RawMessage(`{"wave":1}`)}); err != nil {
				t.Fatal(err)
			}
			bindingRequest(t, h, id, map[string]any{"workspace_id": wid, "plan_path": path, "delivery_id": slug + "-run"}, 200)
		}
	}
	beforeEvents, err := svc.Store.EventsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	beforeMissions, err := svc.Store.ListMissions("")
	if err != nil {
		t.Fatal(err)
	}
	beforeTrees := []map[string]string{bindingTree(t, roots[0]), bindingTree(t, roots[1])}
	for i := range roots {
		for _, slug := range []string{"alpha", "beta"} {
			id := fmt.Sprintf("w%d%s", i, slug)
			view := get(t, h, "/v0/missions/"+id+"/watch").(map[string]any)
			if view["scope"] != "mission" || view["mission_id"] != id || view["delivery"] != slug+"-run" || view["available"] != true {
				t.Fatalf("identity %v", view)
			}
			plans := view["plans"].([]any)
			if len(plans) != 1 || plans[0].(map[string]any)["name"] != slug {
				t.Fatalf("plans %v", plans)
			}
			wantDone := float64(1)
			if slug == "beta" {
				wantDone = 0
			}
			if view["progress"].(map[string]any)["tasks_done"] != wantDone {
				t.Fatalf("progress %v", view)
			}
			waves := view["waves"].([]any)
			rows := waves[0].(map[string]any)["rows"].([]any)
			if rows[0].(map[string]any)["commit"] != id+"-commit" {
				t.Fatalf("foreign evidence %v", view)
			}
		}
	}
	for _, url := range []string{"/v0/missions/w0alpha/watch?workspace=w1", "/v0/missions/w0alpha/watch?delivery=beta-run", "/v0/missions/w0alpha/watch?mission=w0beta", "/v0/watch?mission=w0alpha&workspace=w1"} {
		bindingGetStatus(t, h, url, 400)
	}
	afterEvents, _ := svc.Store.EventsSince(0, 100)
	afterMissions, _ := svc.Store.ListMissions("")
	if !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeMissions, afterMissions) {
		t.Fatal("watch mutated catalog")
	}
	for i, root := range roots {
		if !reflect.DeepEqual(beforeTrees[i], bindingTree(t, root)) {
			t.Fatal("watch mutated artifacts")
		}
	}
}

func bindingTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			out[relative] = "directory"
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[relative] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func bindingGetStatus(t *testing.T, h http.Handler, url string, status int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("%s status %d want %d: %s", url, rec.Code, status, rec.Body.String())
	}
}

func TestMissionBindingValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"missing mission", 404}, {"missing workspace", 404}, {"foreign workspace", 400}, {"missing plan", 404}, {"traversal plan", 400}, {"absolute plan", 400}, {"malformed plan", 422}, {"plan symlink", 422}, {"plan parent symlink", 422}, {"ambiguous plan", 422},
		{"missing journal", 404}, {"traversal delivery", 400}, {"mismatched plan", 422}, {"foreign journal", 422}, {"corrupt journal", 422}, {"duplicate opened", 422}, {"journal symlink", 422}, {"empty journal", 404}, {"store closed", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, svc := testServer(t)
			root := t.TempDir()
			path := bindingFixture(t, root, "alpha", "run")
			if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			id := "m"
			req := map[string]any{"workspace_id": "w", "plan_path": path, "delivery_id": "run"}
			journalPath := filepath.Join(root, journal.Dir, "run.jsonl")
			switch tc.name {
			case "missing mission":
				id = "absent"
			case "missing workspace":
				if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "absent", CreatedAt: domain.Now()}); err != nil {
					t.Fatal(err)
				}
				req["workspace_id"] = "absent"
			case "foreign workspace":
				req["workspace_id"] = "other"
			case "missing plan":
				req["plan_path"] = ".batuta/plans/missing.md"
			case "traversal plan":
				req["plan_path"] = ".batuta/plans/../../secret.md"
			case "absolute plan":
				req["plan_path"] = filepath.Join(root, path)
			case "malformed plan":
				mustBindingWrite(t, filepath.Join(root, path), []byte("secret malformed content"))
			case "plan symlink":
				if err := os.Remove(filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "secret")
				mustBindingWrite(t, target, []byte("DO NOT LEAK"))
				if err := os.Symlink(target, filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
			case "plan parent symlink":
				if err := os.Rename(filepath.Join(root, ".batuta/plans"), filepath.Join(root, ".batuta/moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("moved", filepath.Join(root, ".batuta/plans")); err != nil {
					t.Fatal(err)
				}
			case "ambiguous plan":
				body, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				mustBindingWrite(t, filepath.Join(root, ".batuta/plan-alpha.md"), body)
			case "missing journal":
				req["delivery_id"] = "absent"
			case "traversal delivery":
				req["delivery_id"] = "../../run"
			case "mismatched plan":
				req["plan_path"] = bindingFixture(t, root, "beta", "")
			case "foreign journal":
				other := t.TempDir()
				bindingFixture(t, other, "alpha", "run")
				body, err := os.ReadFile(filepath.Join(other, journal.Dir, "run.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				mustBindingWrite(t, journalPath, body)
			case "corrupt journal":
				body, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				mustBindingWrite(t, journalPath, []byte(strings.Replace(string(body), "alpha", "secret", 1)))
			case "duplicate opened":
				js, err := journal.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				records, err := js.Read("run")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := js.Append("run", records[0]); err != nil {
					t.Fatal(err)
				}
			case "journal symlink":
				if err := os.Rename(journalPath, journalPath+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(journalPath+".real", journalPath); err != nil {
					t.Fatal(err)
				}
			case "empty journal":
				mustBindingWrite(t, journalPath, nil)
			case "store closed":
				if err := svc.Store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := bindingTreeNoLinks(t, root)
			out := bindingRequest(t, h, id, req, tc.status)
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded), "DO NOT LEAK") || strings.Contains(string(encoded), "secret malformed") {
				t.Fatalf("content leak %s", encoded)
			}
			if !reflect.DeepEqual(before, bindingTreeNoLinks(t, root)) {
				t.Fatal("failed binding changed artifacts")
			}
			if tc.name != "store closed" {
				m, err := svc.Store.GetMission("m")
				if err != nil || m.Binding != nil {
					t.Fatalf("failed binding mutated row: %+v %v", m, err)
				}
				events, err := svc.Store.EventsSince(0, 100)
				if err != nil || len(events) != 0 {
					t.Fatalf("failed binding events %v %v", events, err)
				}
			}
		})
	}
}

func mustBindingWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}
func bindingTreeNoLinks(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			out[path] = link
			return err
		}
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			out[path] = string(body)
			return err
		}
		out[path] = "directory"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMissionBindingUnavailableAndImmutable(t *testing.T) {
	for _, mode := range []string{"plan only", "deleted", "changed", "moved with replacement", "journal deleted", "journal replaced", "workspace changed", "archived exact"} {
		t.Run(mode, func(t *testing.T) {
			h, svc := testServer(t)
			root := t.TempDir()
			delivery := "run"
			if mode == "plan only" {
				delivery = ""
			}
			path := bindingFixture(t, root, "alpha", delivery)
			if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
				t.Fatal(err)
			}
			if mode == "archived exact" {
				archive := ".batuta/plans/done/alpha.md"
				if err := os.MkdirAll(filepath.Join(root, ".batuta/plans/done"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(root, path), filepath.Join(root, archive)); err != nil {
					t.Fatal(err)
				}
				path = archive
				bindingFixture(t, root, "alpha", "")
			}
			req := map[string]any{"workspace_id": "w", "plan_path": path, "delivery_id": delivery}
			initial := bindingRequest(t, h, "m", req, 200)
			switch mode {
			case "deleted":
				if err := os.Remove(filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
			case "changed":
				mustBindingWrite(t, filepath.Join(root, path), []byte("# Plan — changed\n**Status:** approved\n- [ ] 1. New task — data/high\n  Accept: different work\n"))
			case "moved with replacement":
				if err := os.MkdirAll(filepath.Join(root, ".batuta/plans/done"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(root, path), filepath.Join(root, ".batuta/plans/done/alpha.md")); err != nil {
					t.Fatal(err)
				}
				mustBindingWrite(t, filepath.Join(root, path), []byte("# Plan — replacement\n**Status:** approved\n- [ ] 1. Replacement — data/high\n  Accept: different work\n"))
			case "journal deleted":
				if err := os.RemoveAll(filepath.Join(root, journal.Dir)); err != nil {
					t.Fatal(err)
				}
			case "journal replaced":
				if err := os.Remove(filepath.Join(root, journal.Dir, "run.jsonl")); err != nil {
					t.Fatal(err)
				}
				bindingFixture(t, root, "alpha", "run")
				js, err := journal.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				records, err := js.Read("run")
				if err != nil {
					t.Fatal(err)
				}
				var detail map[string]any
				if err := json.Unmarshal(records[0].Detail, &detail); err != nil {
					t.Fatal(err)
				}
				detail["branch"] = "replacement-incarnation"
				records[0].Detail, _ = json.Marshal(detail)
				if err := os.Remove(js.Path("run")); err != nil {
					t.Fatal(err)
				}
				if _, err := js.Append("run", records[0]); err != nil {
					t.Fatal(err)
				}
			case "workspace changed":
				if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: t.TempDir(), CreatedAt: domain.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			before := bindingTree(t, root)
			view := get(t, h, "/v0/missions/m/watch").(map[string]any)
			if mode == "archived exact" {
				if view["available"] != true || view["plans"].([]any)[0].(map[string]any)["done"] != true {
					t.Fatalf("archive %v", view)
				}
			} else if view["available"] != false || view["source"] != "unavailable" || view["message"] == nil {
				t.Fatalf("unavailable %v", view)
			}
			if mode == "plan only" {
				if len(view["plans"].([]any)) != 1 {
					t.Fatal(view)
				}
				if _, err := os.Stat(filepath.Join(root, journal.Dir)); !os.IsNotExist(err) {
					t.Fatalf("created journal: %v", err)
				}
			}
			if !reflect.DeepEqual(before, bindingTree(t, root)) {
				t.Fatal("watch wrote artifacts")
			}
			if mode != "plan only" && mode != "workspace changed" {
				again := bindingRequest(t, h, "m", req, 200)
				if !reflect.DeepEqual(initial, again) {
					t.Fatal("idempotent request changed association")
				}
				bindingRequest(t, h, "m", map[string]any{"workspace_id": "w", "plan_path": path}, 409)
			}
			events, err := svc.Store.EventsSince(0, 100)
			if err != nil || len(events) != 1 {
				t.Fatalf("read/idempotence events %v %v", events, err)
			}
		})
	}
}

func TestMissionBindingRejectsTrailingJSON(t *testing.T) {
	h, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPut, "/v0/missions/m/binding", strings.NewReader(`{"workspace_id":"w","plan_path":".batuta/plans/a.md"} {}`))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("trailing JSON status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMissionBindingHTTPConcurrentOwnershipAndRollback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, nil, nil)
	h := (&Server{Svc: svc, Token: "test-token"}).Handler()
	root := t.TempDir()
	path := bindingFixture(t, root, "alpha", "run")
	if err := st.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := st.PutMission(domain.Mission{ID: id, WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_binding_event BEFORE INSERT ON events WHEN NEW.kind='mission.bound' BEGIN SELECT RAISE(ABORT,'sensitive sqlite detail'); END`); err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"workspace_id": "w", "plan_path": path, "delivery_id": "run"}
	failure := bindingRequest(t, h, "a", req, 500)
	encoded, _ := json.Marshal(failure)
	if strings.Contains(string(encoded), "sensitive") {
		t.Fatal("storage detail leaked")
	}
	m, err := st.GetMission("a")
	if err != nil || m.Binding != nil {
		t.Fatalf("HTTP rollback: %+v %v", m, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_binding_event`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(req)
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, id := range []string{"a", "b"} {
		go func() {
			<-start
			r := httptest.NewRequest(http.MethodPut, "/v0/missions/"+id+"/binding", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			results <- rec.Code
		}()
	}
	close(start)
	codes := []int{<-results, <-results}
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{200, 409}) {
		t.Fatalf("ownership HTTP results %v", codes)
	}
	events, err := st.EventsSince(0, 100)
	if err != nil || len(events) != 1 {
		t.Fatalf("events %v %v", events, err)
	}
	for _, endpoint := range []string{"/v0/missions/a/watch", "/v0/watch?mission=a"} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("watch auth %d", rec.Code)
		}
	}
}

func TestMissionBindingArtifactIOHTTP(t *testing.T) {
	h, svc := testServer(t)
	root := t.TempDir()
	path := bindingFixture(t, root, "alpha", "run")
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(root, path)
	permissions := newArtifactPermissions(t, absolute)
	t.Cleanup(func() {
		if err := permissions.chmod(0600); err != nil {
			t.Error(err)
		}
	})
	if err := permissions.chmod(0000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(absolute); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("fixture requires enforced file permissions: %v", err)
	}
	req := map[string]any{"workspace_id": "w", "plan_path": path, "delivery_id": "run"}
	bindingRequest(t, h, "m", req, 503)
	m, err := svc.Store.GetMission("m")
	if err != nil || m.Binding != nil {
		t.Fatalf("IO failure persisted binding: %+v %v", m, err)
	}
	events, err := svc.Store.EventsSince(0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("IO failure event %v %v", events, err)
	}
	if err := permissions.chmod(0600); err != nil {
		t.Fatal(err)
	}
	bindingRequest(t, h, "m", req, 200)
	if err := permissions.chmod(0000); err != nil {
		t.Fatal(err)
	}
	bindingGetStatus(t, h, "/v0/missions/m/watch", 503)
}
