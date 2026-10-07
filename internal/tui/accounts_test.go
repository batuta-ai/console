package tui

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

// Disposable authenticated HTTP over net.Pipe; no provider or personal HOME.
func accountFixture(t *testing.T, handler http.HandlerFunc) *client.Client {
	t.Helper()
	listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer disposable-account-fixture" {
			http.Error(w, "unauthorized", 401)
			return
		}
		handler(w, r)
	})}
	joined := make(chan struct{})
	go func() { defer close(joined); _ = server.Serve(listener) }()
	transport := &http.Transport{DialContext: listener.dial}
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close(); transport.CloseIdleConnections(); <-joined })
	c := client.New("http://account-fixture", "disposable-account-fixture")
	c.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Second}
	return c
}
func accountKey(m model, key string) (model, tea.Cmd) {
	k := tea.KeyPressMsg{Text: key}
	switch key {
	case "enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		k = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		k = tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		k = tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		k = tea.KeyPressMsg{Code: tea.KeyUp}
	}
	next, cmd := m.Update(k)
	return next.(model), cmd
}
func accountComplete(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.BatchMsg); ok {
			t.Fatal("unexpected batch in account action")
		}
		next, nextCmd := m.Update(msg)
		m, cmd = next.(model), nextCmd
	}
	return m
}
func accountText(t *testing.T, m model, want string) {
	t.Helper()
	if !strings.Contains(m.View().Content, want) {
		t.Fatalf("missing %q:\n%s", want, m.View().Content)
	}
}

func TestAccountEmptyAndTwoCreates(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var mu sync.Mutex
			var creates []client.CreateAccountRequest
			var connects []string
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == "POST" && r.URL.Path == "/v0/accounts":
					var in client.CreateAccountRequest
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Error(err)
					}
					creates = append(creates, in)
					_ = json.NewEncoder(w).Encode(domain.Account{ID: "actual-" + in.Label, Label: in.Label, Adapter: in.Adapter, Isolation: domain.IsolationSupported})
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/connect"):
					connects = append(connects, r.URL.Path)
					label := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v0/accounts/actual-"), "/connect")
					_ = json.NewEncoder(w).Encode(domain.Account{ID: "actual-" + label, Label: label, Adapter: provider, Connected: true, Isolation: domain.IsolationSupported})
				default:
					_ = json.NewEncoder(w).Encode([]any{})
				}
			})
			m := New(c).(model)
			accountText(t, m, "n new account")
			var cmd tea.Cmd
			for _, label := range []string{"first", "second"} {
				m, _ = accountKey(m, "n")
				accountText(t, m, "Codex")
				accountText(t, m, "Claude")
				if strings.Contains(m.View().Content, "Label: first") {
					t.Fatal("second form reused label")
				}
				m, cmd = accountKey(m, "enter")
				if cmd != nil {
					t.Fatal("empty label dispatched")
				}
				accountText(t, m, "Enter a label")
				if provider == "claude" {
					m, _ = accountKey(m, "tab")
				}
				m, _ = accountKey(m, label)
				m, cmd = accountKey(m, "enter")
				if cmd == nil {
					t.Fatal("create did not dispatch")
				}
				again, duplicate := accountKey(m, "enter")
				if duplicate != nil {
					t.Fatal("double enter dispatched twice")
				}
				m = again
				m = accountComplete(t, m, cmd)
				accountText(t, m, "actual-"+label)
				m, cmd = accountKey(m, "k")
				if cmd == nil {
					t.Fatal("connect did not dispatch")
				}
				m = accountComplete(t, m, cmd)
				accountText(t, m, "Readiness unknown")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(creates) != 2 || creates[0] != (client.CreateAccountRequest{Label: "first", Adapter: provider}) || creates[1] != (client.CreateAccountRequest{Label: "second", Adapter: provider}) {
				t.Fatalf("wrong creates: %+v", creates)
			}
			if strings.Join(connects, ",") != "/v0/accounts/actual-first/connect,/v0/accounts/actual-second/connect" {
				t.Fatal(connects)
			}
		})
	}
}

func TestAccountReadinessView(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			cases := []struct {
				name      string
				readiness *domain.AccountReadiness
				en, pt    string
			}{
				{"unknown", nil, "Readiness unknown", "Prontidão desconhecida"},
				{"unsupported", &domain.AccountReadiness{}, "Unsupported", "Não suportado"},
				{"missing", &domain.AccountReadiness{Supported: true}, "CLI unavailable", "CLI indisponível"},
				{"incompatible", &domain.AccountReadiness{Supported: true, Available: true}, "CLI incompatible", "CLI incompatível"},
				{"disconnected", &domain.AccountReadiness{Supported: true, Available: true, CLICompatible: true}, "Connect the private home", "Conecte o home privado"},
				{"pending", &domain.AccountReadiness{Supported: true, Available: true, CLICompatible: true, Connected: true}, "Authentication pending", "Autenticação pendente"},
				{"ready", &domain.AccountReadiness{Supported: true, Available: true, CLICompatible: true, Connected: true, Authenticated: true, Ready: true}, "Account ready", "Conta pronta"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					m := New(nil).(model)
					m.screen = scrAccounts
					m.accounts = []domain.Account{{ID: "actual", Label: "Work", Adapter: "codex", Connected: true, Readiness: tc.readiness}}
					m, _ = accountKey(m, "enter")
					want := tc.en
					if lang == "pt" {
						want = tc.pt
					}
					accountText(t, m, want)
				})
			}
		})
	}
}
