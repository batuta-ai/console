package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

const (
	scrWorkspaces           screen = "workspaces"
	scrWorkspace            screen = "workspace"
	workspaceRequestTimeout        = 15 * time.Second
	workspacePickerTimeout         = 2 * time.Minute
)

type workspaceFlow struct {
	draft                              workspaceSetupForm
	sessions                           []domain.Session
	sessionID                          string
	receipts                           []workspaceResult
	selected                           domain.Workspace
	name, root                         string
	form                               bool
	inputRejected                      bool
	missing                            bool
	epoch, revision                    uint64
	pending, read, inspection, refresh *workspaceRequest
	uncertain                          *workspaceRequest
	inspected                          bool
	problem, detail, notice            string
	focus                              int
}

type workspaceResult struct {
	account    *domain.Account
	request    *workspaceRequest
	data       *dataMsg
	workspace  domain.Workspace
	picked     client.PickRootResult
	workspaces []domain.Workspace
	sessions   []domain.Session
	session    domain.Session
	watch      domain.WatchView
	err        error
}

type workspaceRequest struct {
	draft           workspaceSetupForm
	sessionID       string
	clientID        string
	kind            string
	epoch, revision uint64
	workspace       domain.Workspace
	name, root      string
	ctx             context.Context
	cancel          context.CancelFunc
	once            sync.Once
	execute         func(context.Context) workspaceResult
	result          workspaceResult
	lifetime        *terminalLifetime
}

// Navigation may join a command before Bubble Tea schedules it. Dispatch once.
func (r *workspaceRequest) run() tea.Msg {
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
func (m *model) newWorkspaceRequest(kind string) *workspaceRequest {
	if m.workspaceIO == nil {
		m.workspaceIO = &terminalLifetime{}
	}
	timeout := workspaceRequestTimeout
	if kind == "pick" {
		timeout = workspacePickerTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return &workspaceRequest{draft: m.workspace.draft, sessionID: m.workspace.sessionID, clientID: m.clientID, kind: kind, epoch: m.workspace.epoch, revision: m.workspace.revision, workspace: m.workspace.selected, name: strings.TrimSpace(m.workspace.name), root: strings.TrimSpace(m.workspace.root), ctx: ctx, cancel: cancel, lifetime: m.workspaceIO}
}
func workspaceLine(s string, limit int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func workspaceInvalidReply() error {
	return &client.OperationError{Effect: client.EffectUncertain, Code: "workspace_invalid_reply"}
}
func (m model) startWorkspaceForm() model {
	if m.workspace.pending != nil || m.workspace.uncertain != nil {
		return m
	}
	m.workspace.epoch++
	m.workspace.selected = domain.Workspace{}
	m.workspace.name, m.workspace.root = "", ""
	m.workspace.form = true
	m.workspace.draft = workspaceSetupForm{}
	m.workspace.sessionID = ""
	m.workspace.focus = 0
	m.workspace.inputRejected = false
	m.workspace.missing = false
	m.workspace.problem, m.workspace.detail, m.workspace.notice = "", "", ""
	m.screen = scrWorkspace
	return m
}
func (m model) selectWorkspace(w domain.Workspace) model {
	if m.workspace.selected.ID != w.ID || m.workspace.form {
		m.workspace.epoch++
		m.workspace.selected = w
		m.workspace.draft = workspaceSetupForm{}
		m.workspace.sessionID = ""
		m.watch = domain.WatchView{}
		m.workspace.form = false
		m.workspace.focus = 0
		m.workspace.inputRejected = false
		m.workspace.missing = false
		m.workspace.problem, m.workspace.detail = "", ""
	}
	m.screen = scrWorkspace
	return m
}
func (m model) workspaceMutation(kind string) (model, tea.Cmd) {
	if m.exiting || m.workspace.pending != nil || (m.workspace.uncertain != nil && kind != "view") || m.account.pending != nil || m.api == nil {
		return m, nil
	}
	if kind == "create" {
		for _, out := range m.workspace.receipts {
			if out.request != nil && out.request.kind == "create" && out.err != nil && out.request.root == strings.TrimSpace(m.workspace.root) {
				m.workspace.problem = "workspace_unknown_create"
				return m, nil
			}
		}
	}
	if kind == "create" && (m.workspace.inputRejected || !workspaceLine(m.workspace.name, 8192) || !workspaceLine(m.workspace.root, 8192)) {
		m.workspace.problem = "workspace_required"
		return m, nil
	}
	if kind == "init" {
		if m.workspace.inputRejected || !m.workspace.draft.reviewing || !m.workspace.draft.valid() {
			m.workspace.problem = "workspace_config_invalid"
			return m, nil
		}
		if m.workspace.selected.Setup.Ready || m.workspace.selected.Setup.State == "ready" {
			m.workspace.problem = "workspace_already_ready"
			return m, nil
		}
	}
	if (kind == "init" || kind == "view") && m.link != nil && !m.termFrame.disconnected {
		m.workspace.problem = "workspace_detach_first"
		return m, nil
	}
	m.workspace.revision++
	m.account.revision++
	if m.workspace.read != nil {
		m.workspace.read.cancel()
	}
	r := m.newWorkspaceRequest(kind)
	if kind == "view" {
		for _, s := range m.workspace.sessions {
			if s.ID == r.sessionID && s.WorkspaceID == r.workspace.ID {
				r.draft.account = domain.Account{ID: s.AccountID, Adapter: s.Adapter}
				break
			}
		}
	}
	api := m.api
	r.execute = func(ctx context.Context) workspaceResult {
		out := workspaceResult{}
		switch kind {
		case "create":
			out.workspace, out.err = api.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: r.name, Root: r.root})
			if out.err == nil && (!workspaceLine(out.workspace.ID, 8192) || !workspaceLine(out.workspace.Root, 8192)) {
				out.err = workspaceInvalidReply()
			}
		case "init":
			out = workspacePreflight(ctx, api, r)
			if out.err != nil {
				break
			}
			out.session, out.err = api.InitWorkspace(ctx, r.workspace.ID, client.InitWorkspaceRequest{AccountID: r.draft.account.ID, Config: r.draft.config(), Brief: r.draft.fields[8], Owner: "tui", Client: r.clientID})
			if out.err == nil && !workspaceInitSession(out.session, r.workspace.ID, r.draft.account) {
				out.err = workspaceInvalidReply()
			}
		case "view":
			var sessions []domain.Session
			sessions, out.err = api.ListSessions(ctx)
			if out.err == nil {
				out.err = accountPreflightError("workspace_session_unavailable")
				for _, session := range sessions {
					if session.ID == r.sessionID && workspaceInitSession(session, r.workspace.ID, r.draft.account) {
						out.session = session
						out.err = nil
						break
					}
				}
			}
		case "pick":
			local := *api
			httpClient := *api.HTTP
			httpClient.Timeout = workspacePickerTimeout
			local.HTTP = &httpClient
			out.picked, out.err = local.PickWorkspaceRoot(ctx)
			if out.err == nil && !out.picked.Canceled && !workspaceLine(out.picked.Path, 8192) {
				out.err = workspaceInvalidReply()
			}
		}
		return out
	}
	m.workspace.pending = r
	m.workspace.problem, m.workspace.detail, m.workspace.notice = "", "", ""
	return m, r.run
}
func (m *model) readWorkspaces() tea.Cmd {
	if m.exiting || m.api == nil || m.workspace.pending != nil || m.workspace.read != nil {
		return nil
	}
	m.workspace.revision++
	r := m.newWorkspaceRequest("read")
	api := m.api
	r.execute = func(ctx context.Context) workspaceResult {
		out := workspaceResult{}
		out.workspaces, out.err = api.ListWorkspaces(ctx)
		if out.err == nil {
			out.sessions, out.err = api.ListSessions(ctx)
		}
		if out.err == nil && r.workspace.ID != "" {
			out.watch, out.err = api.WorkspaceWatch(ctx, r.workspace.ID, "")
		}
		return out
	}
	m.workspace.read = r
	return r.run
}
func (m *model) inspectWorkspaces() tea.Cmd {
	cmd := m.readWorkspaces()
	m.workspace.inspection = m.workspace.read
	return cmd
}
func (m model) applyWorkspaceResult(out workspaceResult) (model, tea.Cmd) {
	r := out.request
	if r == nil {
		return m, nil
	}
	if r == m.workspace.refresh {
		m.workspace.refresh = nil
		if out.data != nil {
			m = m.applyData(*out.data)
		}
		return m, nil
	}
	if r == m.workspace.read {
		m.workspace.read = nil
		if r.revision != m.workspace.revision || r.epoch != m.workspace.epoch || m.exiting {
			return m, nil
		}
		if out.err != nil {
			m.workspace.problem = "workspace_read_failed"
			m.workspace.detail = out.err.Error()
			return m, nil
		}
		m.workspace.revision++
		m.ws = out.workspaces
		m.workspace.sessions = out.sessions
		m.workspace.missing = m.workspace.selected.ID != ""
		for _, w := range out.workspaces {
			if w.ID == m.workspace.selected.ID {
				m.workspace.selected = w
				m.workspace.missing = false
				break
			}
		}
		if r.workspace.ID == m.workspace.selected.ID {
			m.watch = out.watch
		}
		if r == m.workspace.inspection && m.workspace.uncertain != nil {
			m.workspace.inspected = true
		}
		if m.workspace.uncertain == nil {
			m.workspace.problem, m.workspace.detail = "", ""
		}
		return m, nil
	}
	if r != m.workspace.pending {
		return m, nil
	}
	m.workspace.pending = nil
	if m.workspace.uncertain == r {
		m.workspace.uncertain = nil
	}
	m.workspace.revision++
	m.account.revision++
	current := r.epoch == m.workspace.epoch && !m.exiting
	if current && r.kind == "init" && out.account != nil {
		m.workspace.draft.account = *out.account
		m.workspace.draft.reviewing = false
	}
	if out.err != nil {
		m.workspace.detail = out.err.Error()
		if r.kind == "pick" {
			m.workspace.problem = "workspace_picker_failed"
			return m, nil
		}
		if r.kind == "view" {
			m.workspace.problem = "workspace_session_unavailable"
			return m, nil
		}
		var operation *client.OperationError
		if !errors.As(out.err, &operation) || (operation.Effect != client.EffectNotDispatched && operation.Effect != client.EffectRejected) {
			m.workspace.uncertain = r
			m.workspace.inspected = false
			m.workspace.problem = "workspace_uncertain"
		} else {
			m.workspace.problem = "workspace_retry"
			if i18n.T(operation.Code) != "" {
				m.workspace.problem = operation.Code
			}
		}
		if current && r.kind == "init" {
			m.workspace.draft.reviewing = false
			if out.workspace.ID == r.workspace.ID {
				m.workspace.selected = out.workspace
			}
		}
		return m, nil
	}
	m.workspace.problem, m.workspace.detail = "", ""
	switch r.kind {
	case "pick":
		if current && m.workspace.form {
			if out.picked.Canceled {
				m.workspace.notice = "workspace_picker_cancelled"
			} else {
				m.workspace.root = out.picked.Path
				m.workspace.notice = "workspace_picker_selected"
			}
		}
	case "init", "view":
		m.workspace.receipts = append(m.workspace.receipts, out)
		if r.kind == "init" {
			m.workspace.notice = "workspace_init_started"
		}
		if current {
			return m.attachWorkspaceSession(out.session)
		}
		m.workspace.notice = "workspace_completed_elsewhere"
	case "create":
		m.workspace.receipts = append(m.workspace.receipts, out)
		found := false
		for i, w := range m.ws {
			if w.ID == out.workspace.ID {
				m.ws[i] = out.workspace
				found = true
				break
			}
		}
		if !found {
			m.ws = append(m.ws, out.workspace)
		}
		if current {
			m = m.selectWorkspace(out.workspace)
		} else {
			m.workspace.notice = "workspace_completed_elsewhere"
		}
	}
	return m, nil
}

type workspaceJoined struct {
	results []workspaceResult
}

func (m *model) cancelWorkspaceRequests() tea.Cmd {
	var requests []*workspaceRequest
	for _, r := range []*workspaceRequest{m.workspace.pending, m.workspace.read, m.workspace.refresh} {
		if r != nil {
			r.cancel()
			requests = append(requests, r)
		}
	}
	if r := m.workspace.pending; r != nil && r.kind != "pick" && r.kind != "view" {
		m.workspace.uncertain = r
		m.workspace.inspected = false
		m.workspace.problem = "workspace_uncertain"
	}
	if len(requests) == 0 {
		return nil
	}
	return func() tea.Msg {
		out := workspaceJoined{}
		for _, r := range requests {
			out.results = append(out.results, r.run().(workspaceResult))
		}
		return out
	}
}
func (m model) updateWorkspace(msg tea.KeyPressMsg) (model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m.exitAccountWork(false)
	case "esc":
		if m.workspace.pending == nil && m.workspace.draft.reviewing {
			m.workspace.draft.reviewing = false
			return m, nil
		}
		if m.workspace.pending == nil && m.workspace.draft.editing {
			m.workspace.draft.editing = false
			return m, nil
		}
		m.workspace.epoch++
		m.screen = scrWorkspaces
		return m, m.cancelWorkspaceRequests()
	case "ctrl+a":
		m.workspace.epoch++
		m.screen = scrAccounts
		m.cursor = 0
		return m, m.cancelWorkspaceRequests()
	}
	if m.workspace.pending != nil {
		return m, nil
	}
	if m.workspace.draft.editing {
		return m.updateWorkspaceSetup(msg)
	}
	if m.workspace.form {
		if m.workspace.pending != nil || m.workspace.uncertain != nil {
			return m, nil
		}
		switch key {
		case "tab", "shift+tab":
			m.workspace.focus = 1 - m.workspace.focus
		case "ctrl+p":
			return m.workspaceMutation("pick")
		case "enter":
			return m.workspaceMutation("create")
		default:
			m = m.editWorkspaceText(msg.Text, key == "backspace")
		}
		return m, nil
	}
	switch key {
	case "e":
		if m.workspace.selected.Setup.Ready || m.workspace.selected.Setup.State == "ready" {
			m.workspace.problem = "workspace_already_ready"
			return m, nil
		}
		m.workspace.draft.editing = true
		m.workspace.draft.reviewing = false
	case "[", "]":
		var candidates []domain.Session
		for _, s := range m.workspace.sessions {
			if s.WorkspaceID == m.workspace.selected.ID && s.Work == "init" {
				candidates = append(candidates, s)
			}
		}
		if len(candidates) > 0 {
			index := -1
			for i, s := range candidates {
				if s.ID == m.workspace.sessionID {
					index = i
					break
				}
			}
			step := 1
			if key == "[" {
				step = len(candidates) - 1
			}
			m.workspace.sessionID = candidates[(index+step+len(candidates))%len(candidates)].ID
		}
	case "v":
		for _, s := range m.workspace.sessions {
			if s.ID == m.workspace.sessionID && s.WorkspaceID == m.workspace.selected.ID && s.Work == "init" && s.Process == "running" {
				for _, a := range m.accounts {
					if a.ID == s.AccountID && a.Adapter == s.Adapter {
						return m.workspaceMutation("view")
					}
				}
			}
		}
		m.workspace.problem = "workspace_session_unavailable"
	case "d":
		return m.acknowledgeWorkspace(), nil
	case "f":
		return m, m.inspectWorkspaces()
	case "n":
		next := m.startWorkspaceForm()
		if next.workspace.epoch != m.workspace.epoch {
			return next, next.cancelWorkspaceRequests()
		}
		return next, nil
	case "w":
		m.workspace.epoch++
		m.screen = scrWorkspaces
		m.cursor = 0
		return m, m.cancelWorkspaceRequests()
	case "h":
		m.workspace.epoch++
		m.screen = scrHome
		return m, m.cancelWorkspaceRequests()
	case "q":
		return m.exitAccountWork(false)
	}
	return m, nil
}
func (m model) editWorkspaceText(text string, backspace bool) model {
	if m.workspace.draft.editing {
		if m.workspace.focus >= 9 {
			return m
		}
		if text == "" && !backspace {
			return m
		}
		m.workspace.draft.reviewing = false
		target := &m.workspace.draft.fields[m.workspace.focus]
		if backspace {
			r := []rune(*target)
			if len(r) > 0 {
				*target = string(r[:len(r)-1])
			}
		} else {
			limit := 8193
			if m.workspace.focus == 8 {
				limit = 65537
			}
			if len(*target)+len(text) <= limit {
				*target += text
			} else {
				m.workspace.inputRejected = true
				m.workspace.problem = "workspace_config_invalid"
				return m
			}
		}
		m.workspace.inputRejected = false
		return m
	}
	target := &m.workspace.name
	if m.workspace.focus == 1 {
		target = &m.workspace.root
	}
	if backspace {
		r := []rune(*target)
		if len(r) > 0 {
			*target = string(r[:len(r)-1])
		}
	} else if len(*target)+len(text) <= 8193 {
		*target += text
	} else {
		m.workspace.inputRejected = true
		m.workspace.problem = "workspace_required"
		return m
	}
	m.workspace.inputRejected = false
	return m
}
func (m model) viewWorkspaces(b *strings.Builder) {
	fmt.Fprintln(b, i18n.T("workspaces"))
	for i, w := range m.ws {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		fmt.Fprintf(b, "%s%s  %s  %s  %s\n", mark, w.ID, w.Name, w.Root, workspaceState(w.Setup.State))
	}
	if len(m.ws) == 0 {
		fmt.Fprintln(b, i18n.T("workspace_empty"))
	}
	if len(m.workspace.receipts) > 0 {
		fmt.Fprintln(b, i18n.T("workspace_receipts"))
		for _, out := range m.workspace.receipts {
			r := out.request
			if r == nil {
				continue
			}
			status := i18n.T("workspace_confirmed_result")
			if out.err != nil {
				status = i18n.T("workspace_possible_effect")
			}
			fmt.Fprintf(b, "%s: %s  %s %s  %s %s  %s  %s\n", status, r.kind, r.workspace.ID, r.workspace.Root, r.name, r.root, r.draft.account.ID, out.session.ID)
			if out.workspace.ID != "" {
				fmt.Fprintf(b, "%s  %s\n", out.workspace.ID, out.workspace.Root)
			}
		}
	}
}
func workspaceState(state string) string {
	if s := i18n.T("workspace_state_" + state); s != "" {
		return s
	}
	if state == "" {
		return i18n.T("unknown")
	}
	return state
}
func (m model) viewWorkspace(b *strings.Builder) {
	if m.workspace.form {
		fmt.Fprintln(b, i18n.T("workspace_new"))
		for i, f := range []struct{ key, value string }{{"workspace_name", m.workspace.name}, {"workspace_root", m.workspace.root}} {
			mark := "  "
			if i == m.workspace.focus {
				mark = "> "
			}
			fmt.Fprintf(b, "%s%s: %s\n", mark, i18n.T(f.key), f.value)
		}
		return
	}
	w := m.workspace.selected
	if m.workspace.missing {
		fmt.Fprintln(b, i18n.T("workspace_missing"))
	}
	fmt.Fprintf(b, "%s  %s\n%s\n%s: %s\n", w.ID, w.Name, w.Root, i18n.T("workspace_setup"), workspaceState(w.Setup.State))
	fmt.Fprintf(b, "%s: %s  %s  %s\n", i18n.T("workspace_ready"), accountBool(w.Setup.Ready), w.Setup.Code, w.Setup.ConfigurationCode)
	if a := w.Setup.Attempt; a != nil {
		fmt.Fprintf(b, "%s: %s  %s  %s  %s\n%s: %s  %s\n%s: %s  %s: %v\n", i18n.T("workspace_attempt"), a.SessionID, a.AccountID, a.State, a.Process, i18n.T("workspace_stop_requested"), accountBool(a.StopRequested), a.RecoveryReason, i18n.T("workspace_created"), a.CreatedAt.Format(time.RFC3339), i18n.T("workspace_ended"), a.EndedAt)
	}
	if m.workspace.draft.editing {
		m.viewWorkspaceSetup(b)
	} else {
		for _, s := range m.workspace.sessions {
			if s.WorkspaceID == w.ID && s.Work == "init" {
				marker := "  "
				if s.ID == m.workspace.sessionID {
					marker = "> "
				}
				fmt.Fprintf(b, "%s%s  %s  %s  %s  %s/%s  %s\n", marker, s.ID, s.AccountID, s.Adapter, s.Process, s.Owner, s.OwnerClient, s.RecoveryReason)
			}
		}
	}
}
func (m model) workspaceKeys() string {
	if m.workspace.pending != nil {
		return i18n.T("workspace_pending_keys")
	}
	if m.screen == scrWorkspaces {
		return i18n.T("workspace_list_keys")
	}
	if m.workspace.form {
		return i18n.T("workspace_form_keys")
	}
	if m.workspace.draft.editing {
		return i18n.T("workspace_setup_keys")
	}
	return i18n.T("workspace_detail_keys")
}
func (m model) viewWorkspaceNotice(b *strings.Builder) {
	if m.termMode {
		return
	}
	if m.workspace.pending != nil {
		fmt.Fprintln(b, i18n.T("workspace_pending"))
	}
	if m.workspace.problem != "" {
		fmt.Fprintln(b, i18n.T(m.workspace.problem))
		fmt.Fprintln(b, m.workspace.detail)
	}
	if m.workspace.notice != "" {
		fmt.Fprintln(b, i18n.T(m.workspace.notice))
	}
	if m.screen == scrRoom {
		if s := m.roomSession(); s != nil && s.Work == "init" {
			fmt.Fprintf(b, "%s: %s  %s\n", i18n.T("workspace_session_owner"), s.Owner, s.OwnerClient)
		}
	}
	if r := m.workspace.uncertain; r != nil {
		fmt.Fprintf(b, "%s: %s %s %s %s\n", i18n.T("workspace_possible_effect"), r.kind, r.workspace.ID, r.name, r.root)
		fmt.Fprintf(b, "%s  %s  %s\n", r.workspace.Root, r.draft.account.ID, r.draft.account.Adapter)
		if r.kind == "init" {
			fmt.Fprintf(b, "%+v\n%s\n", r.draft.config(), r.draft.fields[8])
		}
		if m.workspace.pending == nil && r.result.workspace.ID != "" {
			fmt.Fprintf(b, "%s: %s  %s\n", i18n.T("workspace_unverified_reply"), r.result.workspace.ID, r.result.workspace.Root)
		}
		if m.workspace.pending == nil && r.result.session.ID != "" {
			fmt.Fprintf(b, "%s: %s  %s  %s  %s\n", i18n.T("workspace_unverified_reply"), r.result.session.ID, r.result.session.WorkspaceID, r.result.session.AccountID, r.result.session.Adapter)
		}

		if m.screen == scrWorkspace && (m.workspace.form || m.workspace.draft.editing) {
			fmt.Fprintln(b, i18n.T("workspace_inspect_from_form"))
		} else {
			fmt.Fprintln(b, i18n.T("workspace_inspect"))
		}
		if m.workspace.inspected && !m.workspace.form && !m.workspace.draft.editing {
			fmt.Fprintln(b, i18n.T("workspace_acknowledge"))
		}
	}
}

func (m model) acknowledgeWorkspace() model {
	if m.workspace.uncertain != nil && m.workspace.pending == nil && m.workspace.inspected {
		m.workspace.receipts = append(m.workspace.receipts, m.workspace.uncertain.result)
		m.workspace.uncertain = nil
		m.workspace.problem, m.workspace.detail = "", ""
		m.workspace.epoch++
		m.workspace.draft.reviewing = false
		if m.workspace.form {
			m.workspace.form = false
			m.screen = scrWorkspaces
		}
	}
	return m
}
