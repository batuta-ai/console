package tui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

// The workspace/account endpoints below are exact typed synthetic handlers. Terminal
// attachment/control uses the real authenticated API, daemon manager and PTY.
func TestWorkspaceInitActualHTTPWebSocketPTY(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			root := t.TempDir()
			st, err := store.Open(filepath.Join(root, "catalog.db"))
			if err != nil {
				t.Fatal(err)
			}
			manager := session.New(root)
			svc := service.New(st, manager, nil)
			token := "disposable-account-fixture"
			baseHandler := (&api.Server{Svc: svc, Token: token}).Handler()
			for _, id := range []string{"unrelated", "actual-init"} {
				work, account := "mission", "other"
				if id == "actual-init" {
					work, account = "init", "init-account"
				}
				pending, err := manager.StartPending(domain.Session{ID: id, AccountID: account, Adapter: adapter, Work: work, WorkspaceID: map[string]string{"actual-init": "selected-workspace", "unrelated": "other-workspace"}[id], Cols: 80, Rows: 24, Argv: []string{"/bin/sh", "-c", "stty raw -echo; printf INIT_READY; exec /bin/cat"}}, []string{"HOME=" + root}, root, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = pending.Persist(st.PutSession); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.Take("actual-init", "web", "other-client"); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var received client.InitWorkspaceRequest
			listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					http.Error(w, "unauthorized", 401)
					return
				}
				switch r.URL.Path {
				case "/v0/accounts":
					_ = json.NewEncoder(w).Encode([]domain.Account{workspaceReadyAccount("init-account", adapter)})
				case "/v0/capabilities":
					_ = json.NewEncoder(w).Encode(accountCaps())
				case "/v0/workspaces":
					_ = json.NewEncoder(w).Encode([]domain.Workspace{{ID: "selected-workspace", Root: root, Setup: domain.WorkspaceSetup{State: "partial"}}})
				case "/v0/workspaces/selected-workspace/init":
					calls.Add(1)
					_ = json.NewDecoder(r.Body).Decode(&received)
					actual, _ := manager.Snapshot("actual-init")
					_ = json.NewEncoder(w).Encode(actual)
				default:
					baseHandler.ServeHTTP(w, r)
				}
			})}
			joined := make(chan struct{})
			go func() { defer close(joined); _ = server.Serve(listener) }()
			transport := &http.Transport{DialContext: listener.dial}
			c := client.New("http://account-fixture", token)
			c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := manager.Shutdown(ctx); err != nil {
					t.Error(err)
				}
				_ = server.Close()
				_ = listener.Close()
				transport.CloseIdleConnections()
				<-joined
				_ = st.Close()
			})
			m := accountModel(t, c)
			m.ws = []domain.Workspace{{ID: "selected-workspace", Root: root, Setup: domain.WorkspaceSetup{State: "partial"}}}
			m = workspaceReview(t, workspaceSetupDraft(t, m, workspaceConfig(), "native fixture notes"))
			m.sessID = "unrelated"
			m.width = 80
			m.height = 34
			m, cmd := workspaceKey(m, "ctrl+enter")
			again, duplicate := workspaceKey(m, "ctrl+enter")
			m = again
			if duplicate != nil {
				t.Fatal("double init")
			}
			out := cmd()
			next, attach := m.Update(out)
			m = next.(model)
			if m.sessID != "actual-init" || m.termMode || attach == nil {
				t.Fatal("did not choose exact returned init")
			}
			next, wait := m.Update(attach())
			m = next.(model)
			if m.link == nil || wait == nil {
				t.Fatalf("attachment failed: %s", m.validMsg)
			}
			t.Cleanup(func() { m.lifetime.close(); m.accountIO.close(); m.workspaceIO.close() })
			if calls.Load() != 1 || received != (client.InitWorkspaceRequest{AccountID: "init-account", Config: workspaceConfig(), Brief: "native fixture notes", Owner: "tui", Client: m.clientID}) {
				t.Fatalf("init=%d body=%+v", calls.Load(), received)
			}
			frame := terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "INIT_READY") })
			m.termFrame = frame
			if frame.owned {
				t.Fatal("view took control")
			}
			actual, _ := manager.Snapshot("actual-init")
			if actual.OwnerClient != "other-client" {
				t.Fatal("init view transferred existing ownership")
			}
			if m.workspace.selected.Setup.Ready {
				t.Fatal("init response promoted readiness")
			}
			accountText(t, m, "INIT_READY")
			accountText(t, m, "do not prove configuration is ready")
			// Task29 explicit take followed by focus/input, never inferred from init.
			m, _ = accountKey(m, "t")
			m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
			m, _ = accountKey(m, "i")
			m, _ = accountKey(m, "EXACT_INIT_INPUT")
			terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "EXACT_INIT_INPUT") })
			other, _ := manager.Snapshot("unrelated")
			if other.Process != "running" || other.OwnerClient != "" {
				t.Fatal("unrelated session affected")
			}
		})
	}
}

// Real daemon create/list/decoration with an injected picker, over authenticated
// HTTP. No native dialog, provider probe, login or user credentials are involved.
func TestWorkspaceRealDaemonCanonicalRegistration(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := service.New(st, nil, nil)
	var picks, creates atomic.Int32
	handler := (&api.Server{Svc: svc, Token: "disposable-account-fixture", PickDirectory: func(context.Context) (string, bool, error) { picks.Add(1); return root, false, nil }}).Handler()
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/accounts" || r.URL.Path == "/v0/capabilities" || r.URL.Path == "/v0/sessions" {
			accountCatalog(w, r, nil)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v0/workspaces" {
			creates.Add(1)
		}
		handler.ServeHTTP(w, r)
	})
	m := accountModel(t, c)
	m = workspaceCreateDraft(t, m, "original", root+"/.")
	m, cmd := workspaceKey(m, "enter")
	m = accountComplete(t, m, cmd)
	first := m.workspace.selected
	if first.ID == "" || first.Root != root || first.Setup.Ready || first.Setup.State != "missing" {
		t.Fatalf("actual create: %+v", first)
	}
	m, _ = workspaceKey(m, "esc")
	m = workspaceCreateDraft(t, m, "different-name", "")
	m, cmd = workspaceKey(m, "ctrl+p")
	m = accountComplete(t, m, cmd)
	m, cmd = workspaceKey(m, "enter")
	m = accountComplete(t, m, cmd)
	if m.workspace.selected.ID != first.ID || m.workspace.selected.Name != "original" || m.workspace.selected.Root != root {
		t.Fatal("reused canonical workspace rejected or guessed a different identity")
	}
	rows, err := st.ListWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || creates.Load() != 2 || picks.Load() != 1 {
		t.Fatalf("rows=%d creates=%d picks=%d", len(rows), creates.Load(), picks.Load())
	}
}
