package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

const accountRequestTimeout = 15 * time.Second

type accountFlow struct {
	inspection      *accountRequest
	form            bool
	label, adapter  string
	selected        domain.Account
	missing         bool
	epoch, revision uint64
	pending, read   *accountRequest
	uncertain       *accountRequest
	inspected       bool
	problem, notice string
	capabilities    client.CapabilitiesResult
}

type accountResult struct {
	request      *accountRequest
	account      domain.Account
	session      domain.Session
	accounts     []domain.Account
	sessions     []domain.Session
	capabilities client.CapabilitiesResult
	err          error
}

// A command may also be joined by navigation/exit. Once prevents dispatch twice,
// including when cancellation wins before Bubble Tea schedules the command.
type accountRequest struct {
	kind            string
	account         domain.Account
	label, adapter  string
	epoch, revision uint64
	ctx             context.Context
	cancel          context.CancelFunc
	once            sync.Once
	result          accountResult
	execute         func(context.Context) accountResult
	lifetime        *terminalLifetime
}

func (r *accountRequest) run() tea.Msg {
	r.once.Do(func() {
		defer r.cancel()
		parent, finish, ok := r.lifetime.begin()
		if !ok {
			r.result.err = &client.OperationError{Effect: client.EffectNotDispatched, Cause: context.Canceled}
		} else {
			defer finish()
			stop := context.AfterFunc(parent, r.cancel)
			defer stop()
			r.result = r.execute(r.ctx)
		}
		r.result.request = r
	})
	return r.result
}
func (m *model) newAccountRequest(kind string) *accountRequest {
	if m.accountIO == nil {
		m.accountIO = &terminalLifetime{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountRequestTimeout)
	return &accountRequest{kind: kind, account: m.account.selected, label: strings.TrimSpace(m.account.label), adapter: m.account.adapter, epoch: m.account.epoch, revision: m.account.revision, ctx: ctx, cancel: cancel, lifetime: m.accountIO}
}
func (m *model) readAccounts() tea.Cmd {
	if m.exiting || m.account.read != nil || m.account.pending != nil || m.workspace.pending != nil || m.api == nil {
		return nil
	}
	r := m.newAccountRequest("read")
	api := m.api
	r.execute = func(ctx context.Context) accountResult {
		var out accountResult
		out.accounts, out.err = api.ListAccounts(ctx)
		if out.err == nil {
			out.capabilities, out.err = api.Capabilities(ctx)
		}
		if out.err == nil {
			out.sessions, out.err = api.ListSessions(ctx)
		}
		return out
	}
	m.account.read = r
	return r.run
}
func (m *model) inspectAccounts() tea.Cmd {
	cmd := m.readAccounts()
	m.account.inspection = m.account.read
	return cmd
}

func (m model) accountMutation(kind string) (model, tea.Cmd) {
	if m.account.pending != nil || m.workspace.pending != nil || m.account.uncertain != nil || m.exiting {
		return m, nil
	}
	if kind == "create" && strings.TrimSpace(m.account.label) == "" {
		m.account.problem = "account_label_required"
		return m, nil
	}
	if kind != "create" && (m.account.selected.ID == "" || m.account.missing) {
		m.account.problem = "account_missing"
		return m, nil
	}
	if kind == "connect" && m.account.selected.Connected {
		return m, nil
	}
	if kind == "login" && m.link != nil && !m.termFrame.disconnected {
		m.account.problem = "account_detach_first"
		return m, nil
	}
	m.account.revision++
	if m.account.read != nil {
		m.account.read.cancel()
	}
	r := m.newAccountRequest(kind)
	api, identity := m.api, m.clientID
	r.execute = func(ctx context.Context) accountResult {
		out := accountResult{}
		switch kind {
		case "create":
			out.account, out.err = api.CreateAccount(ctx, client.CreateAccountRequest{Label: r.label, Adapter: r.adapter})
			if out.err == nil && (out.account.ID == "" || out.account.Adapter != r.adapter) {
				out.err = accountInvalidReply()
			}
		case "connect":
			out.account, out.err = api.ConnectAccount(ctx, r.account.ID)
			if out.err == nil && (out.account.ID != r.account.ID || out.account.Adapter != r.account.Adapter || !out.account.Connected) {
				out.err = accountInvalidReply()
			}
		case "login":
			// Preflight is read-only. Login itself is dispatched only by this explicit action.
			var accounts []domain.Account
			accounts, out.err = api.ListAccounts(ctx)
			if out.err != nil {
				out.err = &client.OperationError{Effect: client.EffectNotDispatched, Cause: out.err}
				break
			}
			var selected *domain.Account
			for i := range accounts {
				if accounts[i].ID == r.account.ID && accounts[i].Adapter == r.account.Adapter {
					selected = &accounts[i]
					break
				}
			}
			if selected == nil {
				out.err = accountPreflightError("account_missing")
				break
			}
			caps, err := api.Capabilities(ctx)
			if err != nil {
				out.err = &client.OperationError{Effect: client.EffectNotDispatched, Cause: err}
				break
			}
			if problem := accountLoginProblem(*selected, caps); problem != "" {
				out.err = accountPreflightError(problem)
				break
			}
			out.session, out.err = api.LoginAccount(ctx, r.account.ID, client.LoginAccountRequest{Owner: "tui", Client: identity})
			s := out.session
			if out.err == nil && (s.ID == "" || s.AccountID != r.account.ID || s.Adapter != r.account.Adapter || s.Work != "login" || s.Process != "running") {
				out.err = accountInvalidReply()
			}
		}
		return out
	}
	m.account.pending = r
	m.account.problem = ""
	m.account.notice = ""
	return m, r.run
}
func accountInvalidReply() error {
	return &client.OperationError{Effect: client.EffectUncertain, Code: "account_invalid_reply"}
}
func accountPreflightError(code string) error {
	return &client.OperationError{Effect: client.EffectNotDispatched, Code: code}
}
func accountLoginProblem(a domain.Account, c client.CapabilitiesResult) string {
	if !c.PTY.Supported || !c.PTY.Available {
		return "account_pty_unavailable"
	}
	supported := false
	for _, p := range c.Providers {
		if p.ID == a.Adapter {
			supported = p.Supported && p.Installation.Status == "present"
		}
	}
	if !supported {
		return "account_cli_unavailable"
	}
	if !a.Connected {
		return "account_connect_required"
	}
	r := a.Readiness
	if r == nil {
		return "account_unknown"
	}
	if problem := accountCheckProblem(r); problem != "" {
		return problem
	}
	if !r.Supported {
		return "account_unsupported"
	}
	if !r.Available {
		return "account_cli_unavailable"
	}
	if !r.CLICompatible {
		return "account_cli_incompatible"
	}
	return ""
}
func (m model) applyAccountResult(out accountResult) (model, tea.Cmd) {
	r := out.request
	if r == m.account.read {
		m.account.read = nil
		if r.revision != m.account.revision || m.exiting {
			return m, nil
		}
		if out.err != nil {
			m.account.problem = "account_read_failed"
			return m, nil
		}
		m.accounts = out.accounts
		m.updateWorkspaceAccount(out.accounts)
		m.sessions = out.sessions
		m.account.capabilities = out.capabilities
		if m.account.selected.ID != "" {
			m.account.missing = true
			for _, a := range out.accounts {
				if a.ID == m.account.selected.ID && a.Adapter == m.account.selected.Adapter {
					m.account.selected = a
					m.account.missing = false
					break
				}
			}
		}
		m.account.problem = ""
		if m.account.uncertain != nil && m.account.inspection == r {
			m.account.inspected = true
		}
		return m, nil
	}
	if r != m.account.pending {
		return m, nil
	}
	m.account.pending = nil
	if m.account.uncertain == r {
		m.account.uncertain = nil
	}
	m.account.revision++
	current := r.epoch == m.account.epoch && !m.exiting
	if out.err != nil {
		var operation *client.OperationError
		if !errors.As(out.err, &operation) || (operation.Effect != client.EffectNotDispatched && operation.Effect != client.EffectRejected) {
			m.account.uncertain = r
			m.account.inspected = false
			m.account.problem = "account_uncertain"
		} else {
			m.account.problem = "account_retry"
			if i18n.T(operation.Code) != "" {
				m.account.problem = operation.Code
			}
		}
		return m, nil
	}
	m.account.problem = ""
	if r.kind == "create" || r.kind == "connect" {
		// A confirmed result is retained even if the user navigated during dispatch.
		found := false
		for i, a := range m.accounts {
			if a.ID == out.account.ID {
				m.accounts[i] = out.account
				found = true
				break
			}
		}
		if !found {
			m.accounts = append(m.accounts, out.account)
		}
		if current {
			m.account.selected = out.account
			m.account.form = false
			m.account.missing = false
			m.screen = scrAccount
		} else {
			m.account.notice = "account_completed_elsewhere"
		}
		return m, nil
	}
	if r.kind == "login" {
		found := false
		for i, s := range m.sessions {
			if s.ID == out.session.ID {
				m.sessions[i] = out.session
				found = true
				break
			}
		}
		if !found {
			m.sessions = append(m.sessions, out.session)
		}
		m.account.notice = "account_login_started"
		if current && (m.link == nil || m.termFrame.disconnected) {
			m.sessID = out.session.ID
			m.missionID = ""
			m.screen = scrRoom
			m.termMode = false
			m.generation++
			return m, m.attach()
		}
	}
	return m, nil
}
func (m model) startAccountForm() model {
	if m.account.pending != nil || m.account.uncertain != nil {
		return m
	}
	m.account.epoch++
	m.account.form = true
	m.account.label = ""
	m.account.adapter = "codex"
	m.account.selected = domain.Account{}
	m.account.missing = false
	m.account.problem = ""
	m.account.notice = ""
	m.screen = scrAccount
	return m
}
func (m model) updateAccount(msg tea.KeyPressMsg) (model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m.exitAccountWork(false)
	}
	if key == "esc" {
		m.account.epoch++
		m.screen = scrAccounts
		return m, m.cancelAccountRequests()
	}
	if m.account.uncertain != nil && m.account.pending == nil {
		switch key {
		case "f":
			return m, m.inspectAccounts()
		case "d":
			return m.acknowledgeAccount(), nil
		}
	}
	if m.account.form {
		if m.account.pending != nil || m.account.uncertain != nil {
			return m, nil
		}
		switch key {
		case "enter":
			return m.accountMutation("create")
		case "tab", "shift+tab":
			if m.account.adapter == "codex" {
				m.account.adapter = "claude"
			} else {
				m.account.adapter = "codex"
			}
		case "backspace":
			r := []rune(m.account.label)
			if len(r) > 0 {
				m.account.label = string(r[:len(r)-1])
			}
		default:
			m.account.label = accountLabel(m.account.label, msg.Text)
		}
		return m, nil
	}
	switch key {
	case "k":
		return m.accountMutation("connect")
	case "l":
		return m.accountMutation("login")
	case "f":
		return m, m.inspectAccounts()
	case "n":
		return m.startAccountForm(), nil
	case "d":
		return m.acknowledgeAccount(), nil
	case "q":
		return m.exitAccountWork(false)
	}
	return m, nil
}
func accountLabel(existing, text string) string {
	for _, r := range text {
		if !unicode.IsControl(r) && len([]rune(existing)) < 120 {
			existing += string(r)
		}
	}
	return existing
}
func (m model) acknowledgeAccount() model {
	if m.account.uncertain != nil && m.account.inspected && m.account.pending == nil {
		m.account.uncertain = nil
		m.account.problem = ""
		m.account.form = false
		m.screen = scrAccounts
		m.account.epoch++
	}
	return m
}
func (m *model) cancelAccountRequests() tea.Cmd {
	var requests []*accountRequest
	for _, r := range []*accountRequest{m.account.pending, m.account.read} {
		if r != nil {
			r.cancel()
			requests = append(requests, r)
		}
	}
	if m.account.pending != nil {
		m.account.uncertain = m.account.pending
		m.account.inspected = false
		m.account.problem = "account_uncertain"
	}
	if len(requests) == 0 {
		return nil
	}
	return func() tea.Msg {
		out := accountJoined{}
		for _, r := range requests {
			out.results = append(out.results, r.run().(accountResult))
		}
		return out
	}
}

type accountJoined struct {
	workspaces []workspaceResult
	results    []accountResult
	exit       tea.Msg
}

func (m model) exitAccountWork(suspend bool) (model, tea.Cmd) {
	if m.exiting {
		return m, nil
	}
	m.account.epoch++
	m.workspace.epoch++
	workspaceJoin := m.cancelWorkspaceRequests()
	join := m.cancelAccountRequests()
	next, terminalExit := m.exitTerminal(suspend)
	m = next.(model)
	io := m.accountIO
	workspaceIO := m.workspaceIO
	return m, func() tea.Msg {
		io.close()
		workspaceIO.close()
		out := accountJoined{}
		if join != nil {
			out = join().(accountJoined)
		}
		if workspaceJoin != nil {
			out.workspaces = workspaceJoin().(workspaceJoined).results
		}
		out.exit = terminalExit()
		return out
	}
}

// A failed check can leave CLICompatible false without proving incompatibility.
func accountCheckProblem(r *domain.AccountReadiness) string {
	switch r.Code {
	case "probe_unavailable":
		return "account_probe_unavailable"
	case "cli_probe_failed":
		return "account_cli_probe_failed"
	case "check_cancelled":
		return "account_check_cancelled"
	case "isolation_invalid":
		return "account_isolation_invalid"
	case "cli_incompatible":
		return "account_cli_incompatible"
	}
	return ""
}

func accountReadiness(a domain.Account) string {
	r := a.Readiness
	if r == nil {
		return "account_unknown"
	}
	if problem := accountCheckProblem(r); problem != "" {
		return problem
	}
	if !r.Supported {
		return "account_unsupported"
	}
	if !r.Available {
		return "account_cli_unavailable"
	}
	if !r.CLICompatible {
		return "account_cli_incompatible"
	}
	if !r.Connected {
		return "account_connect_required"
	}
	if !r.Authenticated {
		return "account_auth_pending"
	}
	if !r.Ready {
		return "account_not_ready"
	}
	return "account_ready"
}
func accountBool(v bool) string {
	if v {
		return i18n.T("account_yes")
	}
	return i18n.T("account_no")
}
func (m model) viewAccount(b *strings.Builder) {
	if m.account.form {
		fmt.Fprintln(b, i18n.T("account_new"))
		fmt.Fprintf(b, "%s: %s\n", i18n.T("account_label"), m.account.label)
		fmt.Fprintf(b, "%s Codex   %s Claude\n", labelAdapter(m.account.adapter, "codex"), labelAdapter(m.account.adapter, "claude"))
		return
	}
	a := m.account.selected
	fmt.Fprintf(b, "%s  %s  %s\n", a.ID, a.Label, a.Adapter)
	fmt.Fprintf(b, "%s: %s\n%s: %s\n", i18n.T("account_isolation"), a.Isolation, i18n.T("account_connected"), accountBool(a.Connected))
	auth := i18n.T("unknown")
	if a.Readiness != nil && a.Readiness.CLICompatible && accountCheckProblem(a.Readiness) == "" {
		auth = accountBool(a.Readiness.Authenticated)
	}
	fmt.Fprintf(b, "%s: %s\n%s\n", i18n.T("account_authenticated"), auth, i18n.T(accountReadiness(a)))
	if m.account.missing {
		fmt.Fprintln(b, i18n.T("account_missing"))
	}
}
func labelAdapter(actual, want string) string {
	if actual == want {
		return ">"
	}
	return " "
}
func (m model) viewAccountNotice(b *strings.Builder) {
	if m.termMode {
		return
	}
	if m.account.pending != nil {
		fmt.Fprintln(b, i18n.T("account_pending"))
	}
	if m.account.problem != "" {
		fmt.Fprintln(b, i18n.T(m.account.problem))
	}
	if m.account.notice != "" {
		fmt.Fprintln(b, i18n.T(m.account.notice))
	}
	if r := m.account.uncertain; r != nil {
		fmt.Fprintf(b, "%s: %s %s %s %s\n", i18n.T("account_possible_effect"), i18n.T("account_action_"+r.kind), r.account.ID, r.label, r.adapter)
		if m.screen == scrWorkspace || m.screen == scrWorkspaces {
			fmt.Fprintln(b, i18n.T("workspace_account_recovery"))
		} else {
			fmt.Fprintln(b, i18n.T("account_inspect"))
		}
		if m.account.inspected && m.screen != scrWorkspace && m.screen != scrWorkspaces {
			fmt.Fprintln(b, i18n.T("account_acknowledge"))
		}
	}
}

func (m model) viewAccountSessions(b *strings.Builder) {
	if len(m.sessions) == 0 {
		return
	}
	fmt.Fprintln(b, i18n.T("account_sessions"))
	for _, s := range m.sessions {
		marker := "  "
		if s.ID == m.sessID {
			marker = "> "
		}
		fmt.Fprintf(b, "%s%s  %s  %s  %s  %s\n", marker, s.ID, s.AccountID, s.Adapter, s.Work, s.Process)
		if s.RecoveryReason != "" {
			fmt.Fprintln(b, s.RecoveryReason)
		}
	}
	fmt.Fprintln(b, i18n.T("account_session_keys"))
}

func (m model) accountKeys() string {
	if m.screen == scrAccounts {
		if m.account.pending != nil {
			return i18n.T("account_pending_list_keys")
		}
		if m.account.uncertain != nil {
			return i18n.T("account_recovery_list_keys")
		}
		return i18n.T("account_list_keys")
	}
	if m.account.pending != nil {
		return i18n.T("account_pending_keys")
	}
	if m.account.uncertain != nil {
		return i18n.T("account_recovery_keys")
	}
	if m.account.form {
		return i18n.T("account_form_keys")
	}
	return i18n.T("account_detail_keys")
}
