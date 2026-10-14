package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
)

func setupBody(account string) map[string]any {
	return map[string]any{"account_id": account, "brief": "literal notes; $(not-executed)", "config": map[string]any{"stack": "Go", "methodology": "TDD", "test": "go test ./...", "build": "go build ./...", "install": "go mod download", "execution": "sequential", "worktree": "medium+", "template": "templates/go.md"}}
}

func TestExplicitInitHTTPInputAndCatalogErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"missing-account", 409, "authenticated_account_required"},
		{"unknown-account", 404, "init"},
		{"missing-config", 400, "init"},
		{"empty-config-value", 400, "init"},
		{"bad-execution", 400, "init"},
		{"bad-root", 400, "init"},
		{"missing-workspace", 404, "init"},
		{"catalog-failure", 500, "init"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, svc := testServer(t)
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			body := setupBody("absent")
			id := w.ID
			switch tc.name {
			case "missing-account":
				delete(body, "account_id")
			case "missing-config":
				delete(body, "config")
			case "empty-config-value":
				body["config"].(map[string]any)["stack"] = " "
			case "bad-execution":
				body["config"].(map[string]any)["execution"] = "maybe"
			case "bad-root":
				w.Root = filepath.Join(t.TempDir(), "absent")
				if err := svc.Store.PutWorkspace(w); err != nil {
					t.Fatal(err)
				}
			case "missing-workspace":
				id = "absent"
			case "catalog-failure":
				if err := svc.Store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			got := post(t, h, "/v0/workspaces/"+id+"/init", body, tc.status)
			if got["error"] != tc.code {
				t.Fatalf("body=%v", got)
			}
			if tc.name != "catalog-failure" {
				rows, err := svc.Store.ListSessions()
				if err != nil || len(rows) != 0 {
					t.Fatalf("accepted=%v %v", rows, err)
				}
			}
		})
	}
}

func TestSetupHTTPStorageFailureNotCallerError(t *testing.T) {
	h, svc := testServer(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	post(t, h, "/v0/squads", map[string]any{"name": "candidate"}, http.StatusInternalServerError)
	post(t, h, "/v0/workspaces", map[string]string{"name": "candidate", "root": t.TempDir()}, http.StatusInternalServerError)
}

func TestSetupHTTPSelectedReferencesAndReadiness(t *testing.T) {
	for _, kind := range []string{"disconnected", "unsupported", "incompatible", "unauthenticated", "cancelled", "account-table", "events-table", "sessions-table"} {
		t.Run(kind, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "c.db")
			st, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			svc := service.New(st, session.New(t.TempDir()), nil)
			svc.Home = testfixture.PrivateHome(t)
			h := (&Server{Svc: svc, Token: "test-token"}).Handler()
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			a, err := svc.CreateAccount("selected", "codex")
			if err != nil {
				t.Fatal(err)
			}
			cli := testfixture.PrivateHome(t)
			marker := filepath.Join(cli, "marker")
			if err := testfixture.WriteScript(t, filepath.Join(cli, testfixture.ExecutableName("codex")), []byte("#!/bin/sh\necho unexpected > '"+marker+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", cli)
			status, code := 409, "account_disconnected"
			ctx := context.Background()
			switch kind {
			case "unsupported":
				a.Adapter = "opencode"
				if err := svc.Store.PutAccount(a); err != nil {
					t.Fatal(err)
				}
				code = "adapter_unsupported"
			case "incompatible", "unauthenticated":
				a, err = svc.ConnectAccount(a.ID)
				if err != nil {
					t.Fatal(err)
				}
				banner := "codex-cli 0.160.0"
				code = "credential_missing"
				if kind == "incompatible" {
					banner = "codex-cli 99.0.0"
					code = "cli_incompatible"
				}
				script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + banner + "'; exit 0; fi\necho unexpected > '" + marker + "'\n"
				if err := testfixture.RewriteScript(t, filepath.Join(cli, testfixture.ExecutableName("codex")), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				status, code = 503, "check_cancelled"
			case "account-table", "events-table", "sessions-table":
				if kind == "events-table" {
					if err := st.PutSession(domain.Session{ID: "attempt", WorkspaceID: w.ID, Work: "init", Process: "exited", CreatedAt: domain.Now()}); err != nil {
						t.Fatal(err)
					}
				}
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				table := strings.TrimSuffix(kind, "-table")
				if table == "account" {
					table = "accounts"
				}
				if _, err := db.Exec("DROP TABLE " + table); err != nil {
					t.Fatal(err)
				}
				status, code = 500, "init"
			}
			path := "/v0/workspaces/" + w.ID + "/init"
			if kind == "events-table" || kind == "sessions-table" {
				path = "/v0/workspaces"
				code = "store"
			}
			body, err := json.Marshal(setupBody(a.ID))
			if err != nil {
				t.Fatal(err)
			}
			method := http.MethodPost
			if path == "/v0/workspaces" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != status || !strings.Contains(rec.Body.String(), `"error":"`+code+`"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("guard executed provider: %v", err)
			}
		})
	}
}

func TestSetupHTTPExistingSquadStorageErrors(t *testing.T) {
	h, svc := testServer(t)
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	post(t, h, "/v0/squads/existing/validate", map[string]any{}, 500)
	post(t, h, "/v0/squads/existing/save", map[string]any{}, 500)
}

func TestExplicitInitRejectsEveryUnconfirmedField(t *testing.T) {
	for _, field := range []string{"stack", "methodology", "test", "build", "install", "execution", "worktree", "template", "notes-limit", "field-limit", "multiline", "nul"} {
		t.Run(field, func(t *testing.T) {
			h, svc := testServer(t)
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			body := setupBody("selected")
			config := body["config"].(map[string]any)
			switch field {
			case "notes-limit":
				body["brief"] = strings.Repeat("n", 65537)
			case "field-limit":
				config["test"] = strings.Repeat("t", 8193)
			case "multiline":
				config["test"] = "test\nStack: other"
			case "nul":
				config["build"] = "build\x00ignored"
			default:
				config[field] = " "
			}
			seq, err := svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			post(t, h, "/v0/workspaces/"+w.ID+"/init", body, 400)
			after, err := svc.Store.LastEventSeq()
			if err != nil || after != seq {
				t.Fatalf("invalid setup emitted an event: %d %v", after, err)
			}
		})
	}
}
