package api

import (
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
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestReadinessErrorsAreRecoverableBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"session", "init"} {
		t.Run(mode, func(t *testing.T) {
			h, svc := testServer(t)
			cli := testfixture.PrivateHome(t)
			out := filepath.Join(cli, "launch")
			if err := testfixture.WriteScript(t, filepath.Join(cli, testfixture.ExecutableName("codex")), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'codex-cli 0.160.0'; exit 0; fi\necho launched > '"+out+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", cli)
			a, err := svc.CreateAccount("synthetic", "codex")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConnectAccount(a.ID); err != nil {
				t.Fatal(err)
			}
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			q, err := svc.SaveSquad(domain.Squad{Name: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			m, err := svc.CreateMission(w.ID, "synthetic", "", q.ID)
			if err != nil {
				t.Fatal(err)
			}
			seq, err := svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			path, body, code := "/v0/sessions", `{"mission_id":"`+m.ID+`","account_id":"`+a.ID+`","batuta":false}`, "credential_missing"
			if mode == "init" {
				payload, err := json.Marshal(setupBody(a.ID))
				if err != nil {
					t.Fatal(err)
				}
				path, body, code = "/v0/workspaces/"+w.ID+"/init", string(payload), "credential_missing"
			}
			got := post(t, h, path, json.RawMessage(body), http.StatusConflict)
			if got["error"] != code {
				t.Fatalf("nonrecoverable diagnostic: %v", got)
			}
			if got["account_id"] != a.ID {
				t.Fatalf("recovery account missing: %v", got)
			}
			rows, err := svc.Store.ListSessions()
			if err != nil || len(rows) != 0 {
				t.Fatalf("accepted unauthenticated session: %v %v", rows, err)
			}
			events, err := svc.Store.EventsSince(seq, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("unauthenticated start emitted event: %v %v", events, err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("provider executed: %v", err)
			}
		})
	}
}

func TestAccountReadinessUsesRequestCancellation(t *testing.T) {
	h, svc := testServer(t)
	cli := testfixture.PrivateHome(t)
	if err := testfixture.WriteScript(t, filepath.Join(cli, testfixture.ExecutableName("codex")), []byte("#!/bin/sh\n/bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cli)
	a, err := svc.CreateAccount("synthetic", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v0/accounts", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	if time.Since(start) > time.Second || rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code":"check_cancelled"`) {
		t.Fatalf("request cancellation ignored: elapsed=%s status=%d body=%s", time.Since(start), rec.Code, rec.Body.String())
	}
	shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if err := svc.Session.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
}

func TestCLILaunchFailuresHaveRecoveryCodes(t *testing.T) {
	for _, state := range []string{"cli_missing", "cli_incompatible", "adapter_unsupported"} {
		t.Run(state, func(t *testing.T) {
			h, svc := testServer(t)
			cli := testfixture.PrivateHome(t)
			t.Setenv("PATH", cli)
			adapter := "codex"
			if state == "adapter_unsupported" {
				adapter = "gemini"
			}
			if state != "cli_missing" {
				if err := testfixture.WriteScript(t, filepath.Join(cli, testfixture.ExecutableName(adapter)), []byte("#!/bin/sh\necho 'codex-cli 99.0.0'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			a, err := svc.CreateAccount("synthetic", adapter)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConnectAccount(a.ID); err != nil {
				t.Fatal(err)
			}
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			q, err := svc.SaveSquad(domain.Squad{Name: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			m, err := svc.CreateMission(w.ID, "synthetic", "", q.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := post(t, h, "/v0/sessions", map[string]any{"mission_id": m.ID, "account_id": a.ID, "adapter": adapter, "batuta": false}, http.StatusConflict)
			if got["error"] != state {
				t.Fatalf("missing recovery code: %v", got)
			}
			rows, err := svc.Store.ListSessions()
			if err != nil || len(rows) != 0 {
				t.Fatalf("unqualified CLI launch persisted: %v %v", rows, err)
			}
		})
	}
}

func TestSquadReadinessUsesRequestCancellation(t *testing.T) {
	for _, mode := range []string{"stored", "draft", "save"} {
		t.Run(mode, func(t *testing.T) {
			h, svc := testServer(t)
			cli := testfixture.PrivateHome(t)
			if err := testfixture.WriteScript(t, filepath.Join(cli, testfixture.ExecutableName("codex")), []byte("#!/bin/sh\n/bin/sleep 30\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", cli)
			a, err := svc.CreateAccount("synthetic", "codex")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConnectAccount(a.ID); err != nil {
				t.Fatal(err)
			}
			q := domain.Squad{ID: "synthetic", Name: "synthetic", Members: []domain.Member{{ID: "maestro", Role: "Maestro", Maestro: true, CLI: "codex", AccountID: a.ID}}}
			if err := svc.Store.PutSquad(q); err != nil {
				t.Fatal(err)
			}
			path, body := "/v0/squads/"+q.ID+"/validate", []byte(`{}`)
			if mode != "stored" {
				body, err = json.Marshal(q)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "save" {
				path = "/v0/squads"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body))).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			start := time.Now()
			h.ServeHTTP(rec, req)
			if time.Since(start) > time.Second || rec.Code != 200 {
				t.Fatalf("readiness ignored request cancellation: elapsed=%s status=%d body=%s", time.Since(start), rec.Code, rec.Body.String())
			}
			shutdown, done := context.WithTimeout(context.Background(), 3*time.Second)
			defer done()
			if err := svc.Session.Shutdown(shutdown); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountChecksRunIndependentlyWithinSharedBudget(t *testing.T) {
	h, svc := testServer(t)
	cli := testfixture.PrivateHome(t)
	release := filepath.Join(cli, "release")
	script := "#!/bin/sh\nprintf started > \"$HOME/check-started\"\nwhile [ ! -e '" + release + "' ]; do /bin/sleep 0.01; done\necho 'codex-cli 0.160.0'\n"
	if err := testfixture.WriteScript(t, filepath.Join(cli, testfixture.ExecutableName("codex")), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cli)
	var homes []string
	for _, label := range []string{"A", "B"} {
		a, err := svc.CreateAccount(label, "codex")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ConnectAccount(a.ID); err != nil {
			t.Fatal(err)
		}
		homes = append(homes, filepath.Join(svc.Home, "accounts", a.ID))
	}
	req := httptest.NewRequest(http.MethodGet, "/v0/accounts", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(rec, req) }()
	started := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		started = 0
		for _, home := range homes {
			if _, err := os.Stat(filepath.Join(home, "check-started")); err == nil {
				started++
			}
		}
		if started == len(homes) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("account list did not drain")
	}
	if started != len(homes) {
		t.Errorf("later account blocked behind unrelated check: started=%d want=%d", started, len(homes))
	}
	var accounts []domain.Account
	if err := json.Unmarshal(rec.Body.Bytes(), &accounts); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(accounts) != 2 {
		t.Fatalf("account results missing: %d %s", rec.Code, rec.Body.String())
	}
	for _, account := range accounts {
		if account.Readiness == nil || !account.Readiness.CLICompatible || account.Readiness.Code != "credential_missing" {
			t.Fatalf("account check lost its result: %+v", account)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Session.Shutdown(shutdown); err != nil {
		t.Fatal(err)
	}
}
