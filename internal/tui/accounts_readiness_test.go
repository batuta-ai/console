package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

var accountReadinessFailures = []struct {
	code, problem, en, pt string
}{
	{"probe_unavailable", "account_probe_unavailable", "CLI check unavailable. Inspect the daemon diagnostics, then refresh readiness.", "Verificação do CLI indisponível. Inspecione os diagnósticos do daemon e atualize a prontidão."},
	{"cli_probe_failed", "account_cli_probe_failed", "CLI version check failed. Check that the official CLI can report its version, then refresh readiness.", "Falha ao verificar a versão do CLI. Confira se o CLI oficial consegue informar sua versão e atualize a prontidão."},
	{"check_cancelled", "account_check_cancelled", "Readiness check cancelled. Refresh to run a new check before login.", "Verificação de prontidão cancelada. Atualize para executar uma nova verificação antes do login."},
	{"isolation_invalid", "account_isolation_invalid", "Private home isolation is invalid. Inspect the account's private directories before refreshing readiness.", "Isolamento do home privado inválido. Inspecione os diretórios privados da conta antes de atualizar a prontidão."},
	{"cli_incompatible", "account_cli_incompatible", "CLI incompatible. Check the supported official CLI version, then refresh.", "CLI incompatível. Confira a versão suportada do CLI oficial e atualize."},
}

func TestAccountReadinessServerCodesView(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			for _, tc := range accountReadinessFailures {
				t.Run(tc.code, func(t *testing.T) {
					a := accountKnown("actual-account", "codex")
					a.Isolation = domain.IsolationSupported
					a.Readiness.CLICompatible = false
					a.Readiness.Code = tc.code
					m := New(nil).(model)
					m.accounts = []domain.Account{a}
					m, _ = accountKey(m, "c")
					want, auth, connected, isolation := tc.en, "Authenticated (last check): unknown", "Home connected: yes", "Isolation: "
					if lang == "pt" {
						want, auth, connected, isolation = tc.pt, "Autenticada (última verificação): desconhecido", "Home conectado: sim", "Isolamento: "
					}
					if !strings.Contains(m.View().Content, want) {
						t.Errorf("list missing cause/recovery %q:\n%s", want, m.View().Content)
					}
					m, _ = accountKey(m, "enter")
					for _, text := range []string{want, auth, connected, isolation + string(a.Isolation), a.ID} {
						if !strings.Contains(m.View().Content, text) {
							t.Errorf("detail missing %q:\n%s", text, m.View().Content)
						}
					}
					if tc.code != "cli_incompatible" && (strings.Contains(m.View().Content, "CLI incompatible") || strings.Contains(m.View().Content, "CLI incompatível")) {
						t.Error("failed check presented as confirmed incompatibility")
					}
				})
			}
		})
	}
}

func TestAccountDetailOmitsReleaseQualification(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			m := New(nil).(model)
			m.accounts = []domain.Account{accountKnown("actual", "codex")}
			m, _ = accountKey(m, "c")
			m, _ = accountKey(m, "enter")
			if text := m.View().Content; strings.Contains(text, "qualification") || strings.Contains(text, "qualificação") || strings.Contains(text, "Windows") {
				t.Fatalf("release/test status leaked into product:\n%s", text)
			}
		})
	}
}

func TestAccountReadinessTypedPreflightRecovery(t *testing.T) {
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			for _, adapter := range []string{"codex", "claude"} {
				for _, tc := range accountReadinessFailures {
					t.Run(adapter+"/"+tc.code, func(t *testing.T) {
						a := accountKnown("actual-account", adapter)
						var recovered atomic.Bool
						var mu sync.Mutex
						var requests []string
						var logins []client.LoginAccountRequest
						c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
							mu.Lock()
							defer mu.Unlock()
							requests = append(requests, r.Method+" "+r.URL.Path)
							switch {
							case r.Method == "GET" && r.URL.Path == "/v0/accounts":
								fresh := a
								readiness := *a.Readiness
								fresh.Readiness = &readiness
								if !recovered.Load() {
									readiness.CLICompatible, readiness.Code = false, tc.code
								}
								_ = json.NewEncoder(w).Encode([]domain.Account{accountKnown("unrelated", adapter), fresh})
							case r.Method == "GET" && r.URL.Path == "/v0/capabilities":
								_ = json.NewEncoder(w).Encode(accountCaps())
							case r.Method == "GET" && r.URL.Path == "/v0/sessions":
								_ = json.NewEncoder(w).Encode([]domain.Session{})
							case r.Method == "POST" && r.URL.Path == "/v0/accounts/actual-account/login":
								var body client.LoginAccountRequest
								decoder := json.NewDecoder(r.Body)
								decoder.DisallowUnknownFields()
								if err := decoder.Decode(&body); err != nil {
									t.Error(err)
								}
								logins = append(logins, body)
								http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
							default:
								t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
								http.Error(w, "unexpected", 400)
							}
						})
						m := accountModel(t, c)
						m, _ = accountKey(m, "c")
						m, _ = accountKey(m, "down")
						m, _ = accountKey(m, "enter")
						m.account.label, m.account.adapter = "retained-draft", adapter
						m.sessID = "unrelated-session"
						mu.Lock()
						requests = nil
						mu.Unlock()
						m, cmd := accountKey(m, "l")
						if cmd == nil {
							t.Fatal("login preflight not scheduled")
						}
						if _, duplicate := accountKey(m, "l"); duplicate != nil {
							t.Fatal("duplicate preflight scheduled")
						}
						out := cmd().(accountResult)
						var operation *client.OperationError
						if !errors.As(out.err, &operation) || operation.Effect != client.EffectNotDispatched || operation.Code != tc.problem {
							t.Errorf("preflight error = %#v, want not_dispatched/%s", out.err, tc.problem)
						}
						next, follow := m.Update(out)
						m = next.(model)
						if follow != nil || m.account.pending != nil || m.account.uncertain != nil || m.link != nil || m.termMode || m.sessID != "unrelated-session" || m.account.selected.ID != a.ID || m.account.label != "retained-draft" || m.account.adapter != adapter {
							t.Fatal("preflight changed identity/draft/terminal or created uncertainty")
						}
						want := tc.en
						if lang == "pt" {
							want = tc.pt
						}
						if !strings.Contains(m.View().Content, want) {
							t.Errorf("preflight recovery missing %q:\n%s", want, m.View().Content)
						}
						mu.Lock()
						if !reflect.DeepEqual(requests, []string{"GET /v0/accounts", "GET /v0/capabilities"}) || len(logins) != 0 {
							t.Errorf("preflight requests=%v logins=%v", requests, logins)
						}
						mu.Unlock()
						recovered.Store(true)
						m, cmd = accountKey(m, "f")
						m = accountComplete(t, m, cmd)
						mu.Lock()
						if len(logins) != 0 {
							t.Error("refresh dispatched login")
						}
						mu.Unlock()
						if m.account.selected.ID != a.ID || m.account.selected.Readiness.Authenticated || m.account.selected.Readiness.Ready {
							t.Fatal("refresh changed identity or claimed authentication")
						}
						m, cmd = accountKey(m, "l")
						m = accountComplete(t, m, cmd)
						mu.Lock()
						defer mu.Unlock()
						if !reflect.DeepEqual(logins, []client.LoginAccountRequest{{Owner: "tui", Client: m.clientID}}) {
							t.Fatalf("explicit retry wrong typed request: %+v", logins)
						}
					})
				}
			}
		})
	}
}
