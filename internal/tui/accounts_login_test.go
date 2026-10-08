package tui

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestAccountLoginPreflightAndInvalidReplies(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, failure := range []string{"unsupported", "unavailable", "missing", "incompatible", "unknown", "disconnected", "cli_missing", "foreign", "wrong_adapter", "wrong_work", "exited", "malformed", "server_failure"} {
		t.Run(failure, func(t *testing.T) {
			a := accountKnown("actual", "codex")
			caps := accountCaps()
			accounts := []domain.Account{a}
			reply := domain.Session{ID: "login-exact", AccountID: a.ID, Adapter: a.Adapter, Process: "running", Work: "login", Owner: "web", OwnerClient: "other"}
			wantMutation := false
			switch failure {
			case "unsupported":
				caps.PTY.Supported = false
			case "unavailable":
				caps.PTY.Available = false
			case "missing":
				accounts = nil
			case "incompatible":
				a.Readiness.CLICompatible = false
			case "unknown":
				accounts[0].Readiness = nil
			case "disconnected":
				accounts[0].Connected = false
			case "cli_missing":
				caps.Providers[0].Installation.Status = "missing"
			default:
				wantMutation = true
			}
			var mutations atomic.Int32
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v0/accounts":
					_ = json.NewEncoder(w).Encode(accounts)
				case "/v0/capabilities":
					_ = json.NewEncoder(w).Encode(caps)
				case "/v0/accounts/actual/login":
					mutations.Add(1)
					var body client.LoginAccountRequest
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Owner != "tui" || body.Client != "actual-tui-client" {
						t.Errorf("wrong ownership body %+v", body)
					}
					switch failure {
					case "foreign":
						reply.AccountID = "other"
					case "wrong_adapter":
						reply.Adapter = "claude"
					case "wrong_work":
						reply.Work = "mission"
					case "exited":
						reply.Process = "unknown"
					case "malformed":
						io.WriteString(w, `{`)
						return
					case "server_failure":
						http.Error(w, `{"error":"login_failed"}`, 500)
						return
					}
					_ = json.NewEncoder(w).Encode(reply)
				default:
					_ = json.NewEncoder(w).Encode([]domain.Session{})
				}
			})
			for _, id := range []string{"unrelated", "actual-login"} {
				work, account := "mission", "other"
				if id == "actual-login" {
					work, account = "login", "actual-account"
				}
				pending, err := manager.StartPending(domain.Session{ID: id, AccountID: account, Adapter: adapter, Work: work, Cols: 80, Rows: 24, Argv: testfixture.Argv(t, []string{"/bin/sh", "-c", "stty raw -echo; printf LOGIN_READY; exec /bin/cat"})}, testfixture.TerminalEnvironment(t, []string{"HOME=" + root}), root, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = pending.Persist(st.PutSession); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.Take("actual-login", "web", "other-client"); err != nil {
				t.Fatal(err)
			}
			m := accountModel(t, c)
			m.account.selected = accountKnown("actual", "codex")
			m.screen = scrAccount
			m.clientID = "actual-tui-client"
			m.sessID = "unrelated"
			m, cmd := accountKey(m, "l")
			if cmd == nil {
				t.Fatal("explicit login did not start")
			}
			m = accountComplete(t, m, cmd)
			expected := int32(0)
			if wantMutation {
				expected = 1
			}
			if mutations.Load() != expected {
				t.Fatalf("mutations=%d expected=%d", mutations.Load(), expected)
			}
			if m.sessID != "unrelated" || m.link != nil || m.termMode {
				t.Fatal("invalid login authorized attachment/input")
			}
			if m.account.problem == "" {
				t.Fatal("login failure invisible")
			}
			if wantMutation && m.account.uncertain == nil {
				t.Fatal("possible effect lost")
			}
		})
	}
}

// The account endpoints below are exact typed synthetic handlers. Terminal
// attachment/control uses the real authenticated API, daemon manager and PTY.
func TestAccountOfficialLoginOpensActualTerminal(t *testing.T) {
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
			var calls atomic.Int32
			var received client.LoginAccountRequest
			listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					http.Error(w, "unauthorized", 401)
					return
				}
				switch r.URL.Path {
				case "/v0/accounts":
					_ = json.NewEncoder(w).Encode([]domain.Account{accountKnown("actual-account", adapter)})
				case "/v0/capabilities":
					_ = json.NewEncoder(w).Encode(accountCaps())
				case "/v0/accounts/actual-account/login":
					calls.Add(1)
					_ = json.NewDecoder(r.Body).Decode(&received)
					actual, _ := manager.Snapshot("actual-login")
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
			m.screen = scrAccounts
			m, _ = accountKey(m, "enter")
			m.sessID = "unrelated"
			m.width = 80
			m.height = 34
			m, cmd := accountKey(m, "l")
			again, duplicate := accountKey(m, "l")
			m = again
			if duplicate != nil {
				t.Fatal("double login")
			}
			out := cmd()
			next, attach := m.Update(out)
			m = next.(model)
			if m.sessID != "actual-login" || m.termMode || attach == nil {
				t.Fatal("did not choose exact returned login")
			}
			next, wait := m.Update(attach())
			m = next.(model)
			if m.link == nil || wait == nil {
				t.Fatalf("attachment failed: %s", m.validMsg)
			}
			t.Cleanup(func() { m.lifetime.close(); m.accountIO.close() })
			if calls.Load() != 1 || received != (client.LoginAccountRequest{Owner: "tui", Client: m.clientID}) {
				t.Fatalf("login=%d body=%+v", calls.Load(), received)
			}
			frame := terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "LOGIN_READY") })
			m.termFrame = frame
			if frame.owned {
				t.Fatal("view took control")
			}
			actual, _ := manager.Snapshot("actual-login")
			if actual.OwnerClient != "other-client" {
				t.Fatal("login view transferred existing ownership")
			}
			if m.account.selected.Readiness.Authenticated || m.account.selected.Readiness.Ready {
				t.Fatal("login response promoted readiness")
			}
			accountText(t, m, "LOGIN_READY")
			accountText(t, m, "authentication is unconfirmed")
			// Task29 explicit take followed by focus/input, never inferred from login.
			m, _ = accountKey(m, "t")
			m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
			m, _ = accountKey(m, "i")
			m, _ = accountKey(m, "EXACT_LOGIN_INPUT")
			terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "EXACT_LOGIN_INPUT") })
			other, _ := manager.Snapshot("unrelated")
			if other.Process != "running" || other.OwnerClient != "" {
				t.Fatal("unrelated session affected")
			}
		})
	}
}
func TestAccountInspectionShowsActualSessionsAndRecovery(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m := New(nil).(model)
	m.screen = scrAccounts
	m.sessions = []domain.Session{{ID: "actual-unknown", AccountID: "one", Adapter: "codex", Work: "login", Process: "unknown", RecoveryReason: "catalog_only"}, {ID: "actual-live", AccountID: "two", Adapter: "claude", Work: "login", Process: "running"}}
	accountText(t, m, "actual-unknown")
	accountText(t, m, "catalog_only")
	accountText(t, m, "actual-live")
}
