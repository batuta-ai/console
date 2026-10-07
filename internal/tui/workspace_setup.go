package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
	"github.com/batuta-ai/core/loop"
)

type workspaceSetupForm struct {
	fields             [9]string
	account            domain.Account
	editing, reviewing bool
}

var workspaceFieldKeys = [9]string{"workspace_stack", "workspace_methodology", "workspace_test", "workspace_build", "workspace_install", "workspace_execution", "workspace_worktree", "workspace_template", "workspace_notes"}

func (d workspaceSetupForm) config() domain.SetupConfig {
	f := d.fields
	return domain.SetupConfig{Stack: f[0], Methodology: f[1], Test: f[2], Build: f[3], Install: f[4], Execution: f[5], Worktree: f[6], Template: f[7]}
}
func (d workspaceSetupForm) valid() bool {
	names := []string{"Stack", "Methodology", "Test", "Build", "Install", "Execution", "Worktree", "Template"}
	var lines []string
	for i, name := range names {
		if !workspaceLine(d.fields[i], 8192) {
			return false
		}
		lines = append(lines, name+": "+strings.TrimSpace(d.fields[i]))
	}
	return batuta.CompleteProfile(loop.ParseProfile(strings.Join(lines, "\n"))) && len(d.fields[8]) <= 65536 && utf8.ValidString(d.fields[8]) && !strings.ContainsRune(d.fields[8], 0) && d.account.ID != "" && (d.account.Adapter == "codex" || d.account.Adapter == "claude")
}
func (m model) updateWorkspaceSetup(msg tea.KeyPressMsg) (model, tea.Cmd) {
	key := msg.String()
	if m.workspace.pending != nil || m.workspace.uncertain != nil {
		return m, nil
	}
	if key == "ctrl+enter" || (key == "enter" && m.workspace.draft.reviewing) {
		if m.workspace.draft.reviewing {
			return m.workspaceMutation("init")
		}
		return m, nil
	}
	if key == "ctrl+r" {
		if m.workspace.inputRejected || !m.workspace.draft.valid() {
			m.workspace.problem = "workspace_config_invalid"
			return m, nil
		}
		m.workspace.draft.reviewing = true
		m.workspace.problem, m.workspace.detail = "", ""
		return m, nil
	}
	if key == "tab" || key == "shift+tab" {
		step := 1
		if key == "shift+tab" {
			step = 9
		}
		m.workspace.focus = (m.workspace.focus + step) % 10
		return m, nil
	}
	if m.workspace.focus == 9 {
		if key == "left" || key == "right" {
			index := -1
			for i, a := range m.accounts {
				if a.ID == m.workspace.draft.account.ID && a.Adapter == m.workspace.draft.account.Adapter {
					index = i
					break
				}
			}
			if len(m.accounts) > 0 {
				step := 1
				if key == "left" {
					step = len(m.accounts) - 1
				}
				index = (index + step + len(m.accounts)) % len(m.accounts)
				m.workspace.draft.account = m.accounts[index]
				m.workspace.draft.reviewing = false
			}
		}
		return m, nil
	}
	if key == "enter" && m.workspace.focus == 8 {
		return m.editWorkspaceText("\n", false), nil
	}
	if key == "ctrl+u" {
		m.workspace.draft.fields[m.workspace.focus] = ""
		m.workspace.inputRejected = false
		m.workspace.draft.reviewing = false
		return m, nil
	}
	return m.editWorkspaceText(msg.Text, key == "backspace"), nil
}
func workspacePreflight(ctx context.Context, api *client.Client, r *workspaceRequest) workspaceResult {
	out := workspaceResult{}
	workspaces, err := api.ListWorkspaces(ctx)
	if err != nil {
		out.err = workspaceReadPreflight(err)
		return out
	}
	found := false
	for _, w := range workspaces {
		if w.ID == r.workspace.ID {
			out.workspace = w
			found = true
			break
		}
	}
	if !found {
		out.err = accountPreflightError("workspace_missing")
		return out
	}
	if out.workspace.Setup.Ready || out.workspace.Setup.State == "ready" {
		out.err = accountPreflightError("workspace_already_ready")
		return out
	}
	if out.workspace.Setup.State == "invalid_workspace" {
		out.err = accountPreflightError("workspace_invalid_root")
		return out
	}
	accounts, err := api.ListAccounts(ctx)
	if err != nil {
		out.err = workspaceReadPreflight(err)
		return out
	}
	out.account = &domain.Account{ID: r.draft.account.ID, Adapter: r.draft.account.Adapter, Label: r.draft.account.Label}
	var selected domain.Account
	for _, a := range accounts {
		if a.ID == r.draft.account.ID && a.Adapter == r.draft.account.Adapter {
			selected = a
			out.account = &selected
			break
		}
	}
	if selected.ID == "" {
		out.err = accountPreflightError("account_missing")
		return out
	}
	caps, err := api.Capabilities(ctx)
	if err != nil {
		out.err = workspaceReadPreflight(err)
		return out
	}
	if problem := accountLoginProblem(selected, caps); problem != "" {
		out.err = accountPreflightError(problem)
		return out
	}
	if problem := accountReadiness(selected); problem != "account_ready" {
		out.err = accountPreflightError(problem)
		return out
	}
	return out
}
func workspaceReadPreflight(err error) error {
	return &client.OperationError{Effect: client.EffectNotDispatched, Cause: err}
}
func workspaceInitSession(s domain.Session, ws string, a domain.Account) bool {
	return s.ID != "" && s.WorkspaceID == ws && s.AccountID == a.ID && s.Adapter == a.Adapter && s.Work == "init" && s.Process == "running"
}
func (m model) attachWorkspaceSession(s domain.Session) (model, tea.Cmd) {
	if m.link != nil && !m.termFrame.disconnected {
		m.workspace.problem = "workspace_detach_first"
		return m, nil
	}
	found := false
	for i, old := range m.sessions {
		if old.ID == s.ID {
			m.sessions[i] = s
			found = true
			break
		}
	}
	if !found {
		m.sessions = append(m.sessions, s)
	}
	m.sessID = s.ID
	m.missionID = ""
	m.termMode = false
	m.screen = scrRoom
	m.generation++
	return m, m.attach()
}
func (m model) viewWorkspaceSetup(b *strings.Builder) {
	d := m.workspace.draft
	if d.reviewing {
		fmt.Fprintln(b, i18n.T("workspace_confirm"))
	} else {
		fmt.Fprintln(b, i18n.T("workspace_edit"))
	}
	for i, key := range workspaceFieldKeys {
		marker := "  "
		if m.workspace.focus == i {
			marker = "> "
		}
		fmt.Fprintf(b, "%s%s: %s\n", marker, i18n.T(key), d.fields[i])
	}
	marker := "  "
	if m.workspace.focus == 9 {
		marker = "> "
	}
	fmt.Fprintf(b, "%s%s: %s  %s  %s\n", marker, i18n.T("workspace_account"), d.account.ID, d.account.Label, d.account.Adapter)
	if d.account.ID != "" {
		fmt.Fprintln(b, i18n.T(accountReadiness(d.account)))
	} else {
		fmt.Fprintln(b, i18n.T("workspace_select_account"))
	}
	fmt.Fprintln(b, i18n.T("workspace_choices"))
}

func (m *model) updateWorkspaceAccount(accounts []domain.Account) {
	old := m.workspace.draft.account
	if old.ID == "" {
		return
	}
	for _, a := range accounts {
		if a.ID == old.ID && a.Adapter == old.Adapter {
			if accountReadiness(old) != accountReadiness(a) || old.Connected != a.Connected {
				m.workspace.draft.reviewing = false
			}
			m.workspace.draft.account = a
			return
		}
	}
	m.workspace.draft.account.Readiness = nil
	m.workspace.draft.reviewing = false
}
