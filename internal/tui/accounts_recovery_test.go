package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func accountCaps() client.CapabilitiesResult {
	return client.CapabilitiesResult{PTY: domain.PTYCapability{Supported: true, Available: true}, Providers: []domain.CLICapability{{ID: "codex", Supported: true, Installation: domain.CapabilityFact{Status: "present"}}, {ID: "claude", Supported: true, Installation: domain.CapabilityFact{Status: "present"}}}}
}
func accountKnown(id, adapter string) domain.Account {
	return domain.Account{ID: id, Label: id, Adapter: adapter, Connected: true, Readiness: &domain.AccountReadiness{Supported: true, Available: true, CLICompatible: true, Connected: true}}
}
func accountModel(t *testing.T, c *client.Client) model {
	t.Helper()
	m := New(c).(model)
	if m.account.read != nil {
		m = accountComplete(t, m, m.account.read.run)
	}
	return m
}
func accountCatalog(w http.ResponseWriter, r *http.Request, accounts []domain.Account) {
	switch r.URL.Path {
	case "/v0/accounts":
		_ = json.NewEncoder(w).Encode(accounts)
	case "/v0/capabilities":
		_ = json.NewEncoder(w).Encode(accountCaps())
	default:
		_ = json.NewEncoder(w).Encode([]domain.Session{})
	}
}
func accountDraft(t *testing.T, m model) (model, tea.Cmd) {
	t.Helper()
	m, _ = accountKey(m, "n")
	m, _ = accountKey(m, "draft")
	return accountKey(m, "enter")
}
func TestAccountUncertainRecovery(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, failure := range []string{"400", "409", "500", "malformed", "foreign", "lost"} {
		t.Run(failure, func(t *testing.T) {
			var mutations atomic.Int32
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					mutations.Add(1)
					switch failure {
					case "400":
						http.Error(w, `{"error":"invalid"}`, 400)
					case "409":
						http.Error(w, `{"error":"conflict"}`, 409)
					case "500":
						http.Error(w, `{"error":"failed"}`, 500)
					case "malformed":
						io.WriteString(w, `{"id":`)
					case "foreign":
						_ = json.NewEncoder(w).Encode(domain.Account{ID: "foreign", Adapter: "claude"})
					case "lost":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
					}
					return
				}
				accountCatalog(w, r, []domain.Account{{ID: "must-not-guess", Label: "draft", Adapter: "codex"}})
			})
			m := accountModel(t, c)
			m.screen = scrHome
			m, cmd := accountDraft(t, m)
			m = accountComplete(t, m, cmd)
			accountText(t, m, "Possible effect")
			for _, key := range []string{"enter", "n", "draft", "enter"} {
				m, cmd = accountKey(m, key)
				if cmd != nil {
					t.Fatal("uncertain mutation replayed")
				}
			}
			// Inspection must also be reachable while an uncertain create form is shown.
			m, cmd = accountKey(m, "f")
			if cmd == nil {
				t.Fatal("uncertain form cannot inspect")
			}
			m = accountComplete(t, m, cmd)
			if m.account.selected.ID != "" {
				t.Fatal("matched a label to an unknown create")
			}
			m, _ = accountKey(m, "d")
			if m.screen != scrAccounts {
				t.Fatal("manual recovery did not return to exact selection")
			}
			m, _ = accountKey(m, "enter")
			if m.account.selected.ID != "must-not-guess" {
				t.Fatal("manual actual selection unavailable")
			}
			if mutations.Load() != 1 {
				t.Fatalf("%d mutations", mutations.Load())
			}
		})
	}
}
func TestAccountPartialConnectAndSafeRetry(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var creates, connects atomic.Int32
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v0/accounts" {
			creates.Add(1)
			_ = json.NewEncoder(w).Encode(domain.Account{ID: "created-exact", Label: "draft", Adapter: "codex"})
			return
		}
		if r.Method == "POST" {
			if r.URL.Path != "/v0/accounts/created-exact/connect" {
				t.Errorf("wrong identity %s", r.URL.Path)
			}
			connects.Add(1)
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	m, cmd := accountDraft(t, m)
	m = accountComplete(t, m, cmd)
	m, cmd = accountKey(m, "k")
	m = accountComplete(t, m, cmd)
	accountText(t, m, "created-exact")
	accountText(t, m, "Request not dispatched or rejected")
	m, cmd = accountKey(m, "k")
	if cmd == nil {
		t.Fatal("safe explicit retry unavailable")
	}
	m = accountComplete(t, m, cmd)
	if creates.Load() != 1 || connects.Load() != 2 {
		t.Fatal("partial progress recreated account")
	}
}
func TestAccountNavigationCancelsAndJoins(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			count.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			defer close(finished)
			<-release
			_ = json.NewEncoder(w).Encode(domain.Account{ID: "late", Adapter: "codex"})
			return
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	m, cmd := accountDraft(t, m)
	returned := make(chan tea.Msg, 1)
	go func() { returned <- cmd() }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request not dispatched")
	}
	m, dup := accountKey(m, "enter")
	if dup != nil {
		t.Fatal("pending double dispatch")
	}
	m, join := accountKey(m, "esc")
	if join == nil {
		t.Fatal("navigation did not join")
	}
	m = accountComplete(t, m, join)
	select {
	case out := <-returned:
		m = accountComplete(t, m, func() tea.Msg { return out })
	case <-time.After(time.Second):
		t.Fatal("owned command not joined")
	}
	close(release)
	<-finished
	if m.screen != scrAccounts || m.account.selected.ID != "" || m.account.uncertain == nil {
		t.Fatal("canceled request lost uncertainty or selected stale result")
	}
	m, _ = accountKey(m, "n")
	if m.screen != scrAccounts {
		t.Fatal("new draft erased possible effect")
	}
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
}
func TestAccountCancelBeforeDispatchAndSuspend(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var mutations atomic.Int32
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			mutations.Add(1)
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	m, late := accountDraft(t, m)
	next, exit := m.Update(terminalExitRequest{suspend: true})
	m = next.(model)
	out := exit().(accountJoined)
	// Observe joined state without actually suspending the test host.
	for _, result := range out.results {
		m, _ = m.applyAccountResult(result)
	}
	if mutations.Load() != 0 {
		t.Fatal("canceled command dispatched")
	}
	if m.account.uncertain != nil {
		t.Fatal("not-dispatched result retained false uncertainty")
	}
	next, _ = m.Update(terminalResumed{})
	m = next.(model)
	m = accountComplete(t, m, late)
	if mutations.Load() != 0 || m.account.pending != nil {
		t.Fatal("late command survived suspend")
	}
}
func TestAccountStaleReadAndMissingCatalog(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	var mode atomic.Int32
	a := accountKnown("one", "codex")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 1 {
			http.Error(w, `{"error":"read_failed"}`, 500)
			return
		}
		accounts := []domain.Account{a}
		if mode.Load() == 2 {
			accounts = nil
		}
		accountCatalog(w, r, accounts)
	})
	m := accountModel(t, c)
	m.screen = scrAccounts
	m, _ = accountKey(m, "enter")
	mode.Store(1)
	m, cmd := accountKey(m, "f")
	m = accountComplete(t, m, cmd)
	if len(m.accounts) != 1 || m.account.selected.ID != "one" {
		t.Fatal("failed read destroyed confirmed identity")
	}
	accountText(t, m, "Read failed")
	mode.Store(2)
	m, cmd = accountKey(m, "f")
	m = accountComplete(t, m, cmd)
	accountText(t, m, "Account absent")
	if m.account.selected.ID != "one" {
		t.Fatal("missing catalog destroyed identity")
	}
	if _, cmd = accountKey(m, "l"); cmd != nil {
		t.Fatal("missing account authorized login")
	}
	// A read started before a mutation cannot overwrite the confirmed account.
	mode.Store(0)
	m, cmd = accountKey(m, "f")
	old := cmd()
	m.account.revision++
	m.account.selected = accountKnown("two", "claude")
	next, _ := m.Update(old)
	m = next.(model)
	if m.account.selected.ID != "two" {
		t.Fatal("stale read replaced selection")
	}
}
func TestAccountFormUsesTextFocus(t *testing.T) {
	m := New(nil).(model)
	m, _ = accountKey(m, "n")
	for _, key := range []string{"q", "c", "h", "s", "n"} {
		next, cmd := accountKey(m, key)
		m = next
		if cmd != nil || m.exiting || m.screen != scrAccount {
			t.Fatal("text invoked navigation")
		}
	}
	next, _ := m.Update(tea.PasteMsg{Content: "safe\n\x1b[31m"})
	m = next.(model)
	if strings.ContainsAny(m.account.label, "\n\x1b") {
		t.Fatal("control text in label")
	}
}

func TestAccountAutomaticReadCannotAcknowledgeUncertainty(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, `{"error":"lost"}`, 500)
			return
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	m, cmd := accountDraft(t, m)
	m = accountComplete(t, m, cmd)
	// Periodic observation is not the user's explicit inspection/recovery choice.
	cmd = m.readAccounts()
	m = accountComplete(t, m, cmd)
	if m.account.inspected {
		t.Fatal("automatic poll enabled acknowledgment")
	}
	m, _ = accountKey(m, "d")
	if m.account.uncertain == nil {
		t.Fatal("automatic poll cleared uncertainty")
	}
	m, _ = accountKey(m, "esc")
	m, _ = accountKey(m, "h")
	m, cmd = accountKey(m, "f")
	if cmd == nil {
		t.Fatal("visible global inspection hint not actionable")
	}
	m = accountComplete(t, m, cmd)
	m, _ = accountKey(m, "d")
	if m.account.uncertain != nil {
		t.Fatal("explicit inspection acknowledgment unavailable")
	}
}
func TestAccountDelayedSuccessDoesNotSwitchSelection(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	a, b := accountKnown("one", "codex"), accountKnown("two", "claude")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewEncoder(w).Encode(domain.Session{ID: "confirmed-login", AccountID: a.ID, Adapter: a.Adapter, Process: "running", Work: "login"})
			return
		}
		accountCatalog(w, r, []domain.Account{a, b})
	})
	m := accountModel(t, c)
	m.screen = scrAccounts
	m, _ = accountKey(m, "enter")
	m, cmd := accountKey(m, "l")
	out := cmd() // server finished, but completion has not reached the UI
	m, join := accountKey(m, "esc")
	m, _ = accountKey(m, "down")
	m, _ = accountKey(m, "enter")
	m = accountComplete(t, m, join)
	next, attach := m.Update(out)
	m = next.(model)
	if m.account.selected.ID != "two" || m.sessID != "" || attach != nil || m.termMode || m.account.uncertain != nil {
		t.Fatal("stale success selected/authorized another account")
	}
	found := false
	for _, s := range m.sessions {
		if s.ID == "confirmed-login" {
			found = true
		}
	}
	if !found {
		t.Fatal("late confirmed session identity lost")
	}
}
func TestAccountReadDuringNewDraftRetainsDraft(t *testing.T) {
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		accountCatalog(w, r, []domain.Account{accountKnown("one", "codex")})
	})
	m := accountModel(t, c)
	cmd := m.readAccounts()
	old := cmd()
	m, _ = accountKey(m, "n")
	m, _ = accountKey(m, "tab")
	m, _ = accountKey(m, "new-label")
	next, _ := m.Update(old)
	m = next.(model)
	if !m.account.form || m.account.label != "new-label" || m.account.adapter != "claude" || m.account.selected.ID != "" {
		t.Fatal("catalog replaced draft")
	}
}

func TestAccountNonemptyHomeNavigation(t *testing.T) {
	m := New(nil).(model)
	m.ws = []domain.Workspace{{ID: "existing", Name: "Existing"}}
	m, _ = accountKey(m, "enter")
	if m.screen != scrMissions {
		t.Fatal("onboarding intercepted existing workspace navigation")
	}
}
func TestAccountWhitespaceLabelAndBoundedInput(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m := New(nil).(model)
	m, _ = accountKey(m, "n")
	m, _ = accountKey(m, "   ")
	m, cmd := accountKey(m, "enter")
	if cmd != nil {
		t.Fatal("whitespace label dispatched")
	}
	accountText(t, m, "Enter a label")
	next, _ := m.Update(tea.PasteMsg{Content: strings.Repeat("é", 10000)})
	m = next.(model)
	if len([]rune(m.account.label)) > 120 {
		t.Fatal("unbounded label")
	}
}

func TestAccountLeavingListJoinsRead(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { accountCatalog(w, r, nil) })
	m := accountModel(t, c)
	m.screen = scrAccounts
	entered := make(chan struct{})
	c.HTTP.Transport = terminalRoundTrip(func(r *http.Request) (*http.Response, error) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("unbounded read")
		}
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	cmd := m.readAccounts()
	returned := make(chan tea.Msg, 1)
	go func() { returned <- cmd() }()
	<-entered
	m, join := accountKey(m, "h")
	if join == nil {
		m.account.read.cancel()
		<-returned
		t.Fatal("leaving accounts did not join its read")
	}
	m = accountComplete(t, m, join)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("read not joined")
	}
	if m.screen != scrHome || m.account.read != nil {
		t.Fatal("navigation state not preserved")
	}
}

func TestAccountPendingAndUncertainHints(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, `{"error":"lost"}`, 500)
			return
		}
		accountCatalog(w, r, nil)
	})
	m := accountModel(t, c)
	m, cmd := accountDraft(t, m)
	if strings.Contains(m.View().Content, "enter create") {
		t.Fatal("pending form advertises an enabled create action")
	}
	m = accountComplete(t, m, cmd)
	if strings.Contains(m.View().Content, "Type label") {
		t.Fatal("uncertain form advertises editing")
	}
	m.termMode = true
	m.screen = scrRoom
	if strings.Contains(m.View().Content, "f inspect") {
		t.Fatal("panel inspection hint leaked into child focus")
	}
}
func TestAccountErrorViewsBothLanguages(t *testing.T) {
	cases := []struct{ key, en, pt string }{
		{"account_read_failed", "Read failed", "Falha na leitura"},
		{"account_uncertain", "Possible effect", "Efeito possível"},
		{"account_retry", "Request not dispatched or rejected", "Solicitação não enviada ou rejeitada"},
		{"account_missing", "Account absent", "Conta ausente"},
		{"account_pty_unavailable", "Runtime PTY unavailable", "PTY indisponível"},
		{"account_login_started", "authentication is unconfirmed", "autenticação não confirmada"},
	}
	for _, lang := range []string{"en", "pt"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("BATUTA_LANG", lang)
			for _, tc := range cases {
				m := New(nil).(model)
				m.screen = scrAccount
				m.account.problem = tc.key
				want := tc.en
				if lang == "pt" {
					want = tc.pt
				}
				accountText(t, m, want)
			}
		})
	}
}
