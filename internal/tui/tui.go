package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
	"github.com/mattn/go-isatty"
)

type screen string

const (
	scrHome     screen = "home"
	scrMissions screen = "missions"
	scrRoom     screen = "room"
	scrSquads   screen = "squads"
	scrEditor   screen = "editor"
	scrAccounts screen = "accounts"
	scrAccount  screen = "account"
	scrActivity screen = "activity"
	scrReview   screen = "review"
)

type model struct {
	journey     journeyFlow
	journeyIO   *terminalLifetime
	catalog     catalogFlow
	catalogIO   *terminalLifetime
	workspace   workspaceFlow
	workspaceIO *terminalLifetime

	account    accountFlow
	accountIO  *terminalLifetime
	api        *client.Client
	clientID   string
	screen     screen
	termMode   bool
	ws         []domain.Workspace
	missions   []domain.Mission
	squads     []domain.Squad
	accounts   []domain.Account
	activity   []domain.Event
	sessions   []domain.Session
	watch      domain.WatchView
	limits     []domain.LimitSnapshot
	cursor     int
	width      int
	height     int
	missionID  string
	sessID     string
	generation uint64
	terminal   *terminalScreen
	termFrame  terminalFrame
	link       *termLink
	lifetime   *terminalLifetime
	exiting    bool
	exitReady  bool
	editor     domain.Squad
	editIdx    int
	validMsg   string
}

func New(api *client.Client) tea.Model {
	m := model{api: api, screen: scrHome, clientID: clientID(), lifetime: &terminalLifetime{}, accountIO: &terminalLifetime{}, workspaceIO: &terminalLifetime{}, catalogIO: &terminalLifetime{}}
	m.readAccounts()
	m.refresh()
	return m
}

func (m model) Init() tea.Cmd {
	var read tea.Cmd
	if m.account.read != nil {
		read = m.account.read.run
	}
	return tea.Batch(m.refresh(), read, func() tea.Msg { return catalogReadStart{} }, m.tick())
}

func (m model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

type tickMsg struct{}

func (m *model) refresh() tea.Cmd {
	if m.api == nil || m.exiting {
		return nil
	}
	var previous *workspaceRequest
	if m.workspace.refresh != nil {
		if m.workspace.refresh.epoch == m.workspace.epoch && m.workspace.refresh.revision == m.workspace.revision {
			return m.workspace.refresh.run
		}
		previous = m.workspace.refresh
		previous.cancel()
	}
	r := m.newWorkspaceRequest("refresh")
	api, missionWorkspaceID, accountRevision := m.api, m.roomMission().WorkspaceID, m.account.revision
	r.execute = func(ctx context.Context) workspaceResult {
		if previous != nil {
			previous.run()
		}
		msg := dataMsg{workspaceRead: true, workspaceEpoch: r.epoch, workspaceRevision: r.revision, accountRevision: accountRevision}
		msg.ws, msg.workspacesErr = api.ListWorkspaces(ctx)
		msg.activity, _ = api.Activity(ctx)
		msg.sessions, msg.sessionsErr = api.ListSessions(ctx)
		msg.limits, _ = api.Limits(ctx)
		wsID := r.workspace.ID
		if wsID == "" {
			if len(msg.ws) > 0 {
				wsID = msg.ws[0].ID
			}
			if missionWorkspaceID != "" {
				wsID = missionWorkspaceID
			}
		}
		if wsID != "" {
			msg.watch, msg.watchErr = api.WorkspaceWatch(ctx, wsID, "")
		}
		msg.watchWorkspaceID = wsID
		return workspaceResult{data: &msg}
	}
	m.workspace.refresh = r
	return r.run
}

type dataMsg struct {
	workspaceRead                     bool
	workspaceEpoch, workspaceRevision uint64
	workspacesErr, watchErr           error
	watchWorkspaceID                  string

	sessionsErr     error
	accountRevision uint64
	ws              []domain.Workspace
	activity        []domain.Event
	sessions        []domain.Session
	limits          []domain.LimitSnapshot
	watch           domain.WatchView
}

type statusMsg string

func (m model) applyData(msg dataMsg) model {
	m.activity = msg.activity
	m.limits = msg.limits
	if msg.workspaceRead && msg.workspaceEpoch == m.workspace.epoch && msg.workspaceRevision == m.workspace.revision && !m.exiting {
		if msg.workspacesErr == nil {
			m.ws = msg.ws
			m.workspace.missing = m.workspace.selected.ID != ""
			for _, w := range msg.ws {
				if w.ID == m.workspace.selected.ID {
					m.workspace.selected = w
					m.workspace.missing = false
					break
				}
			}
		} else {
			m.workspace.problem = "workspace_read_failed"
			m.workspace.detail = msg.workspacesErr.Error()
		}
		if !m.journeyVisible() && msg.watchErr == nil && (m.workspace.selected.ID == "" || msg.watchWorkspaceID == m.workspace.selected.ID) {
			m.watch = msg.watch
		}
		if msg.watchErr != nil {
			m.workspace.problem = "workspace_read_failed"
			m.workspace.detail = msg.watchErr.Error()
		}
		if msg.workspacesErr == nil && msg.watchErr == nil && m.workspace.problem == "workspace_read_failed" {
			m.workspace.problem, m.workspace.detail = "", ""
		}
	}
	if msg.sessionsErr == nil && msg.accountRevision == m.account.revision {
		m.sessions = msg.sessions
	}
	return m
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	current := next.(model)
	if (m.journeyVisible() && !current.journeyVisible()) || m.missionID != current.missionID || m.roomMission().WorkspaceID != current.roomMission().WorkspaceID {
		cmd = tea.Batch(cmd, current.cancelJourney())
	}
	if (current.screen != m.screen || current.workspace.selected.ID != m.workspace.selected.ID) && current.catalog.epoch == m.catalog.epoch {
		current.catalog.epoch++
		current.catalog.validation = nil
		return current, tea.Batch(cmd, current.cancelCatalogRequests())
	}
	return current, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case journeyResult:
		return m.applyJourney(msg)
	case catalogReadStart:
		return m, m.readCatalog()
	case catalogResult:
		return m.applyCatalogResult(msg)
	case catalogJoined:
		for _, result := range msg.results {
			m, _ = m.applyCatalogResult(result)
		}
		return m, nil
	case workspaceResult:
		return m.applyWorkspaceResult(msg)
	case workspaceJoined:
		for _, result := range msg.results {
			m, _ = m.applyWorkspaceResult(result)
		}
		return m, nil
	case accountResult:
		return m.applyAccountResult(msg)
	case accountJoined:
		if msg.journey != nil {
			m, _ = m.applyJourney(*msg.journey)
		}
		for _, result := range msg.catalog {
			m, _ = m.applyCatalogResult(result)
		}
		for _, result := range msg.results {
			m, _ = m.applyAccountResult(result)
		}
		for _, result := range msg.workspaces {
			m, _ = m.applyWorkspaceResult(result)
		}
		if msg.exit != nil {
			return m.Update(msg.exit)
		}
		return m, nil
	case terminalExitRequest:
		return m.exitAccountWork(msg.suspend)
	case terminalExitReady:
		m.exitReady = true
		if msg.suspend {
			return m, terminalSuspend()
		}
		return m, tea.Quit
	case terminalResumed:
		m.exiting, m.exitReady, m.termMode = false, false, false
		m.lifetime = &terminalLifetime{}
		m.accountIO = &terminalLifetime{}
		m.workspaceIO = &terminalLifetime{}
		m.catalogIO = &terminalLifetime{}
		m.journeyIO = &terminalLifetime{}
		m.link = nil
		m.termFrame.disconnected = true
		m.termFrame.owned = false
		m.termFrame.problem = "disconnected after suspend; explicit reconnect required"
		if msg.err != nil {
			m.termFrame.problem = msg.err.Error()
		}
		return m, nil
	case tea.WindowSizeMsg:
		inspectionResized := m.width != msg.Width || m.height != msg.Height
		m.width, m.height = msg.Width, msg.Height
		if inspectionResized {
			for key, r := range m.journey.records {
				r.page, r.confirmPage, r.inspected = 0, 0, 0
				if m.journeyVisible() && key == journeyID(m.roomMission()) && r.mode == "source" && r.confirm == "" && r.detail != nil {
					r.inspected = min(m.journeyBodyRows(r), len(journeySourceLines(*r.detail, m.width)))
				}
				m.saveJourney(key, r)
			}
		}
		if m.termMode {
			return m, m.sendResize(msg.Width, msg.Height-10)
		}
	case dataMsg:
		m = m.applyData(msg)
	case tickMsg:
		read := m.readAccounts()
		refresh, catalog := m.refresh(), m.readCatalog()
		var journeyRefresh tea.Cmd
		// Keep the progress snapshot stable; explicit reads and operation results still apply.
		if m.journeyRecord().mode != "progress" {
			journeyRefresh = m.readJourney("refresh")
		}
		return m, tea.Batch(refresh, read, catalog, journeyRefresh, m.tick())
	case statusMsg:
		m.validMsg = string(msg)
	case attachedMsg:
		if m.exiting || msg.id != m.sessID || msg.clientID != m.clientID || msg.generation != m.generation {
			if msg.link != nil {
				msg.link.cancel()
			}
			return m, nil
		}
		if msg.err != nil {
			m.validMsg = msg.err.Error()
			return m, nil
		}
		m.link = msg.link
		m.termMode = false
		m.screen = scrRoom
		return m, m.waitPTY()
	case ptyOutMsg:
		if m.terminal == nil {
			m.terminal = &terminalScreen{}
		}
		cols, rows := 0, 0
		for _, session := range m.sessions {
			if session.ID == m.sessID {
				cols, rows = session.Cols, session.Rows
				break
			}
		}
		engine, id := m.terminal, m.sessID
		return m, func() tea.Msg { return terminalFrameMsg{sessionID: id, frame: engine.feed(cols, rows, string(msg))} }
	case terminalFrameMsg:
		if msg.sessionID != m.sessID || msg.generation != m.generation || msg.clientID != m.clientID {
			return m, nil
		}
		m.termFrame = msg.frame
		if msg.frame.disconnected {
			m.termMode = false
			return m, nil
		}
		return m, m.waitPTY()
	case tea.PasteMsg:
		if !m.termMode && m.journeyVisible() && m.journeyRecord().mode != "" {
			return m.editJourney(msg.Content, false), nil
		}
		if !m.termMode && (m.screen == scrEditor || m.screen == scrMissionForm) {
			return m.editCatalogText(msg.Content, false), nil
		}
		if !m.termMode && m.screen == scrWorkspace && (m.workspace.form || m.workspace.draft.editing) && m.workspace.pending == nil && m.workspace.uncertain == nil {
			return m.editWorkspaceText(msg.Content, false), nil
		}
		if !m.termMode && m.screen == scrAccount && m.account.form && m.account.pending == nil && m.account.uncertain == nil {
			m.account.label = accountLabel(m.account.label, msg.Content)
		}
		if m.termMode && m.link != nil {
			m.link.offer(terminalAction{paste: &msg.Content})
		}
		return m, nil
	case tea.FocusMsg:
		if m.termMode && m.link != nil {
			focused := true
			m.link.offer(terminalAction{focus: &focused})
		}
		return m, nil
	case tea.BlurMsg:
		if m.termMode && m.link != nil {
			focused := false
			m.link.offer(terminalAction{focus: &focused})
		}
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+]":
			if m.link != nil && !m.termFrame.disconnected {
				m.termMode = false
				return m, m.detach()
			}
		case "esc":
			if m.termMode {
				return m, m.sendPTY("\x1b")
			}
		}
		if m.termMode {
			if m.link != nil {
				m.link.offer(terminalAction{key: &msg})
			}
			return m, nil
		}
		if m.journeyVisible() {
			if pending := m.journey.pending; pending != nil && pending.kind == "refresh" {
				pending.cancel()
				m.journey.pending = nil
			}
			if next, cmd, handled := m.updateJourney(msg); handled {
				return next, cmd
			}
		}
		if m.screen == scrEditor {
			return m.updateEditor(msg)
		}
		if m.screen == scrMissionForm {
			return m.updateMissionForm(msg)
		}
		if msg.String() == "ctrl+f" {
			return m, m.inspectCatalog()
		}
		if msg.String() == "ctrl+d" {
			return m.acknowledgeCatalog(), nil
		}
		if m.screen == scrWorkspace {
			return m.updateWorkspace(msg)
		}
		if m.screen != scrWorkspaces && m.account.uncertain != nil && m.account.pending == nil {
			switch msg.String() {
			case "f":
				return m, m.inspectAccounts()
			case "d":
				return m.acknowledgeAccount(), nil
			}
		}
		if m.screen == scrAccount {
			return m.updateAccount(msg)
		}
		if m.screen == scrAccounts {
			switch msg.String() {
			case "f":
				return m, m.inspectAccounts()
			case "d":
				return m.acknowledgeAccount(), nil
			}
		}
		previousScreen := m.screen
		switch msg.String() {
		case "ctrl+c", "q":
			return m.exitAccountWork(false)
		case "down":
			m.cursor++
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			m = m.open()
		case "f":
			if m.screen == scrWorkspaces {
				return m, m.inspectWorkspaces()
			}
		case "d":
			if m.screen == scrWorkspaces {
				return m.acknowledgeWorkspace(), nil
			}
		case "ctrl+a":
			if m.screen == scrWorkspaces {
				m.screen, m.cursor = scrAccounts, 0
			}
		case "w":
			m.screen, m.cursor = scrWorkspaces, 0
		case "m":
			m.screen, m.cursor = scrMissions, 0
		case "s":
			m.screen, m.cursor = scrSquads, 0
		case "c":
			m.screen, m.cursor = scrAccounts, 0
		case "a":
			m.screen, m.cursor = scrActivity, 0
		case "r":
			m.screen, m.cursor = scrReview, 0
		case "h":
			m.screen, m.cursor = scrHome, 0
		case "t":
			if m.link != nil {
				m.link.offer(terminalAction{operation: "tui"})
			}
			return m, nil
		case "v":
			if m.link != nil && !m.termFrame.disconnected {
				m.validMsg = "detach the current attachment first"
				return m, nil
			}
			m.generation++
			return m, m.attach()
		case "i":
			if m.link != nil && m.termFrame.owned {
				m.termMode = true
			}
			return m, nil
		case "u":
			if m.link != nil {
				m.link.offer(terminalAction{operation: "none"})
			}
			return m, nil
		case "x":
			if m.screen == scrRoom && m.link != nil {
				m.link.offer(terminalAction{operation: "stop"})
			}
			return m, nil
		case "pgup", "pgdown":
			if m.link != nil {
				n := 10
				if msg.String() == "pgdown" {
					n = -10
				}
				m.link.offer(terminalAction{history: n})
			}
			return m, nil
		case "[", "]":
			if m.link == nil || m.termFrame.disconnected {
				ids := []string{}
				for _, s := range m.sessions {
					if m.missionID == "" || s.MissionID == m.missionID {
						ids = append(ids, s.ID)
					}
				}
				for i, id := range ids {
					if m.sessID == id {
						step := 1
						if msg.String() == "[" {
							step = -1
						}
						m.sessID = ids[(i+step+len(ids))%len(ids)]
						return m, nil
					}
				}
				if len(ids) > 0 {
					m.sessID = ids[0]
				}
			}
			return m, nil
		case "n":
			if m.screen == scrWorkspaces {
				next := m.startWorkspaceForm()
				if next.workspace.epoch != m.workspace.epoch {
					return next, next.cancelWorkspaceRequests()
				}
				return next, nil
			}
			if m.screen == scrHome || m.screen == scrAccounts {
				return m.startAccountForm(), nil
			}
			if m.screen == scrMissions {
				return m.startMission(), m.cancelCatalogRequests()
			}
			if m.screen == scrSquads {
				return m.startSquad(domain.Squad{}), m.cancelCatalogRequests()
			}
		}
		if previousScreen != m.screen {
			m.catalog.epoch++
			m.catalog.validation = nil
			catalogJoin := m.cancelCatalogRequests()
			if previousScreen == scrAccounts && m.screen != scrAccount {
				m.account.epoch++
				return m, tea.Batch(catalogJoin, m.cancelAccountRequests())
			}
			if previousScreen == scrWorkspaces {
				m.workspace.epoch++
				return m, tea.Batch(catalogJoin, m.cancelWorkspaceRequests())
			}
			if m.screen == scrRoom {
				return m, tea.Batch(catalogJoin, m.cancelWorkspaceRequests())
			}
			return m, catalogJoin
		}
	}
	return m, nil
}

func (m model) open() model {
	switch m.screen {
	case scrWorkspaces:
		if m.cursor >= 0 && m.cursor < len(m.ws) {
			return m.selectWorkspace(m.ws[m.cursor])
		}
	case scrHome:
		if len(m.accounts) == 0 && len(m.ws) == 0 {
			return m.startAccountForm()
		}
		m.screen = scrMissions
	case scrMissions:
		if m.cursor >= 0 && m.cursor < len(m.missions) {
			return m.selectMission(m.missions[m.cursor])
		}
	case scrSquads:
		if m.cursor >= 0 && m.cursor < len(m.squads) {
			return m.startSquad(m.squads[m.cursor])
		}
	case scrAccounts:
		if m.cursor >= 0 && m.cursor < len(m.accounts) {
			m.account.epoch++
			m.account.selected = m.accounts[m.cursor]
			m.account.form, m.account.missing = false, false
			m.screen = scrAccount
		}
	case scrActivity:
		m.screen = scrRoom
	}
	return m
}

func (m model) View() tea.View {
	return tea.NewView(m.render())
}

func (m model) render() string {
	var b strings.Builder
	nav := fmt.Sprintf("%s  workspace  %s | %s | %s | %s | %s",
		i18n.T("title"), label(m.screen, scrHome, i18n.T("home")), label(m.screen, scrMissions, i18n.T("missions")),
		label(m.screen, scrSquads, i18n.T("squads")), label(m.screen, scrAccounts, i18n.T("accounts")), label(m.screen, scrActivity, i18n.T("activity")))
	fmt.Fprintln(&b, nav)
	fmt.Fprintln(&b, rule(m.width))
	switch m.screen {
	case scrWorkspaces:
		m.viewWorkspaces(&b)
	case scrWorkspace:
		m.viewWorkspace(&b)
	case scrHome:
		m.viewHome(&b)
	case scrMissions:
		m.viewMissions(&b)
	case scrRoom:
		m.viewRoom(&b)
	case scrSquads:
		m.viewSquads(&b)
	case scrEditor:
		m.viewEditor(&b)
	case scrMissionForm:
		m.viewMissionForm(&b)
	case scrAccounts:
		m.viewAccounts(&b)
	case scrAccount:
		m.viewAccount(&b)
	case scrActivity:
		m.viewActivity(&b)
	case scrReview:
		m.viewReview(&b)
	}
	inspection := m.journeyInspectionVisible()
	if !inspection {
		m.viewCatalogNotice(&b)
		m.viewAccountNotice(&b)
		m.viewWorkspaceNotice(&b)
	}
	panel := asciiFold(b.String())
	b.Reset()
	var terminal strings.Builder
	if m.screen == scrRoom && !inspection {
		if m.validMsg != "" {
			fmt.Fprintln(&terminal, m.validMsg)
		}
		if m.termFrame.text != "" {
			fmt.Fprintln(&terminal, "")
			fmt.Fprintln(&terminal, m.termFrame.render(m.width, max(0, m.height-10)))
		}
		if m.termFrame.provisional {
			fmt.Fprintln(&terminal, "provisional byte replay; use an explicit child repaint if needed")
		}
		if m.termFrame.control != "" {
			fmt.Fprintln(&terminal, m.termFrame.control)
		}
		if m.termFrame.held {
			fmt.Fprintln(&terminal, "synchronized output held; history unavailable")
		}
		if m.termFrame.problem != "" {
			fmt.Fprintln(&terminal, m.termFrame.problem)
		}
	}
	fmt.Fprintln(&b, rule(m.width))
	if m.termMode {
		fmt.Fprintln(&b, i18n.T("term"))
	} else if m.screen == scrWorkspace || m.screen == scrWorkspaces {
		fmt.Fprintln(&b, m.workspaceKeys())
	} else if m.screen == scrAccount || m.screen == scrAccounts {
		fmt.Fprintln(&b, m.accountKeys())
	} else if m.screen == scrEditor {
		fmt.Fprintln(&b, i18n.T("catalog_editor_keys"))
	} else if m.screen == scrMissionForm {
		fmt.Fprintln(&b, i18n.T("catalog_mission_keys"))
	} else if m.screen == scrSquads || m.screen == scrMissions {
		fmt.Fprintln(&b, i18n.T("catalog_list_keys"))
	} else {
		fmt.Fprintln(&b, i18n.T("keys"))
	}
	return panel + terminal.String() + asciiFold(b.String())
}

func (m model) viewHome(b *strings.Builder) {
	fmt.Fprintln(b, i18n.T("workspace_home_keys"))
	if len(m.accounts) == 0 {
		fmt.Fprintln(b, i18n.T("account_empty"))
	}
	fmt.Fprintf(b, "\n%s\n\n", i18n.T("home"))
	if line := m.watch.AttentionLine(); line != "" {
		fmt.Fprintf(b, "%s  %s\n\n", i18n.T("needs_you"), line)
	}
	if len(m.ws) == 0 {
		fmt.Fprintln(b, "no workspaces yet")
		return
	}
	for i, w := range m.ws {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		fmt.Fprintf(b, "%s%s  %s\n", mark, w.Name, w.Root)
	}
}

func (m model) viewMissions(b *strings.Builder) {
	fmt.Fprintf(b, "\nOBJECTIVE            SQUAD        STATUS\n")
	for i, msn := range m.missions {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		fmt.Fprintf(b, "%s%-20s %-12s %s\n", mark, clip(msn.Title, 20), clip(msn.SquadSnapshot.Name, 12), msn.Status+"  "+msn.ID+"  "+msn.WorkspaceID)
	}
	if len(m.missions) == 0 {
		fmt.Fprintln(b, "  no missions")
	}
}

func (m model) viewRoom(b *strings.Builder) {
	m.viewMissionIdentity(b)
	if m.viewJourney(b) {
		return
	}
	title := i18n.T("room")
	if msn := m.roomMission(); msn.ID != "" {
		title = msn.Title
	}
	for _, candidate := range m.sessions {
		if m.missionID == "" || candidate.MissionID == m.missionID {
			marker := "  "
			if candidate.ID == m.sessID {
				marker = "> "
			}
			fmt.Fprintf(b, "%s%s  member=%s  account=%s  workspace=%s\n", marker, candidate.ID, candidate.MemberID, candidate.AccountID, candidate.WorkspaceID)
		}
	}
	sess := m.roomSession()
	if sess != nil && sess.MissionID != m.missionID {
		fmt.Fprintln(b, i18n.T("journey_unrelated"), sess.ID, sess.MissionID)
	}
	fmt.Fprintf(b, "\n%s\n", title)
	if line := m.watch.AttentionLine(); line != "" {
		fmt.Fprintf(b, "%s  %s\n", i18n.T("needs_you"), line)
	}
	if sess != nil {
		fmt.Fprintf(b, "process=%s (%s)  client=%s  work=%s\n", sess.Process, domain.ProcessLife(sess.Process), sess.Owner, sess.Work)
	} else {
		fmt.Fprintln(b, "process=-  client=none  work=-")
	}
	owner := "none"
	if sess != nil {
		owner = sess.Owner
	}
	if m.watch.Available {
		fmt.Fprintf(b, "delivery %s\n", m.watch.Delivery)
	} else if m.watch.Message != "" {
		fmt.Fprintln(b, m.watch.Message)
	}
	fmt.Fprintf(b, "%s   progress is verified gates\n", m.watch.FormatGates())
	fmt.Fprintln(b, "TASK                 STATE       G0 G1 G2 G3")
	rows := m.watch.TaskRows()
	if len(rows) == 0 {
		fmt.Fprintln(b, "  no journal tasks")
		for _, mb := range m.roomMission().SquadSnapshot.Members {
			fmt.Fprintf(b, "  %-18s %-11s %s\n", clip(mb.Role, 18), mb.CLI, mb.AccountID)
		}
	}
	for _, r := range rows {
		label := r.Title
		if label == "" {
			label = r.Task
		}
		fmt.Fprintf(b, "  %-18s %-11s %s %s %s %s\n", clip(label, 18), clip(r.State, 11), r.Gates[0], r.Gates[1], r.Gates[2], r.Gates[3])
	}
	if owner == "tui" && m.termMode {
		fmt.Fprintln(b, "TUI CONTROLS INPUT  [ctrl+] navigation]")
	} else {
		fmt.Fprintln(b, "[ / ] select  v view/reconnect  t take  u release  i input  x stop  ctrl+] detach")
	}

}

func (m model) roomSession() *domain.Session {
	for i := range m.sessions {
		s := &m.sessions[i]
		if m.sessID != "" && s.ID == m.sessID {
			return s
		}
	}
	return nil
}

func (m model) viewSquads(b *strings.Builder) {
	fmt.Fprintf(b, "\n%s  %s  [n new]\n", i18n.T("squads"), i18n.T("save"))
	for i, q := range m.squads {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		state := "draft"
		if !q.Draft {
			state = fmt.Sprintf("v%d", q.Version)
		}
		fmt.Fprintf(b, "%s%-20s %s  %d members\n", mark, clip(q.Name, 20)+"  "+q.ID, state, len(q.Members))
	}
}

func (m model) viewAccounts(b *strings.Builder) {
	fmt.Fprintf(b, "\n%s\n", i18n.T("accounts"))
	fmt.Fprintln(b, i18n.T("account_columns"))
	for i, a := range m.accounts {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		lim := i18n.T("unknown")
		bar := "[..........]"
		for _, snap := range m.limits {
			if snap.AccountID != a.ID {
				continue
			}
			lim = domain.FormatLimit(snap)
			if snap.Percent != nil {
				bar = limitBar(*snap.Percent)
			} else {
				bar = "[----------]"
			}
			break
		}
		fmt.Fprintf(b, "%s%-20s %-18s %-24s %s\n", mark, clip(a.Label, 20), a.Isolation, clip(lim, 24), bar)
		fmt.Fprintf(b, "  %s  %s\n", a.Adapter, i18n.T(accountReadiness(a)))
	}
	if len(m.accounts) == 0 {
		fmt.Fprintln(b, i18n.T("account_empty"))
	}
	fmt.Fprintln(b, i18n.T("account_connection_help"))
	m.viewAccountSessions(b)
}

func limitBar(p float64) string {
	n := int(p / 10)
	if n < 0 {
		n = 0
	}
	if n > 10 {
		n = 10
	}
	return "[" + strings.Repeat("#", n) + strings.Repeat(".", 10-n) + "]"
}

func (m model) viewActivity(b *strings.Builder) {
	fmt.Fprintf(b, "\n%s\n", i18n.T("activity"))
	for i, e := range m.activity {
		if i > 12 {
			break
		}
		fmt.Fprintf(b, "  %s  %s\n", e.At.Format("15:04:05"), e.Kind)
	}
	fmt.Fprintln(b, "\nSESSION              PROCESS     CLIENT   WORK")
	for _, s := range m.sessions {
		fmt.Fprintf(b, "  %-18s %-11s %-8s %s\n", clip(s.ID, 18), s.Process, s.Owner, s.Work)
		if s.Process == "unknown" {
			fmt.Fprintf(b, "    %s\n", domain.ProcessLife(s.Process))
		}
	}
	fmt.Fprintln(b, "disconnecting a client does not stop the process")
}

func (m model) viewReview(b *strings.Builder) {
	if m.viewJourney(b) {
		return
	}
	fmt.Fprintf(b, "\n%s\n", i18n.T("review"))
	if m.watch.Available {
		fmt.Fprintf(b, "delivery %s\n", m.watch.Delivery)
	} else if m.watch.Message != "" {
		fmt.Fprintln(b, m.watch.Message)
	}
	fmt.Fprintln(b, "\nCRITERIA")
	crit := m.watch.Criteria()
	if len(crit) == 0 {
		fmt.Fprintln(b, "  no criteria in this journal")
	}
	for _, c := range crit {
		fmt.Fprintf(b, "  C%d  %-8s %s\n", c.Index, c.State, c.Title)
	}
	fmt.Fprintln(b, "\nEVIDENCE")
	ev := m.watch.Evidence()
	if len(ev) == 0 {
		fmt.Fprintln(b, "  evidence comes from the Batuta journal when present")
	}
	for _, e := range ev {
		fmt.Fprintf(b, "  %-8s  %s  (%s)\n", e.Kind, clip(e.Text, 48), e.Origin)
	}
}

func label(cur, want screen, name string) string {
	if cur == want {
		return "[" + name + "]"
	}
	return name
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func Run(api *client.Client) error {
	m := New(api).(model)
	if !isatty.IsTerminal(os.Stdout.Fd()) || os.Getenv("TERM") == "dumb" {
		if m.account.read != nil {
			m, _ = m.applyAccountResult(m.account.read.run().(accountResult))
		}
		defer m.accountIO.close()
		defer m.workspaceIO.close()
		defer m.catalogIO.close()
		if read := m.readCatalog(); read != nil {
			m, _ = m.applyCatalogResult(read().(catalogResult))
		}
		msg := m.refresh()()
		if result, ok := msg.(workspaceResult); ok {
			m, _ = m.applyWorkspaceResult(result)
		}
		_, _ = fmt.Fprint(os.Stdout, m.render())
		return nil
	}
	return runTerminalProgram(m)
}

func runTerminalProgram(m model, options ...tea.ProgramOption) error {
	defer m.lifetime.close()
	defer m.accountIO.close()
	defer m.workspaceIO.close()
	defer m.catalogIO.close()
	options = append(options, tea.WithoutSignalHandler(), tea.WithFilter(func(current tea.Model, msg tea.Msg) tea.Msg {
		state := current.(model)
		switch msg.(type) {
		case tea.QuitMsg, tea.InterruptMsg:
			if !state.exitReady {
				return terminalExitRequest{}
			}
		case tea.SuspendMsg:
			return terminalExitRequest{suspend: true}
		}
		return msg
	}))
	p := tea.NewProgram(m, options...)
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, terminalSignals()...)
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		for {
			select {
			case <-done:
				return
			case sig := <-signals:
				p.Send(terminalExitRequest{suspend: terminalIsSuspend(sig)})
			}
		}
	}()
	defer func() { signal.Stop(signals); close(done); <-joined }()
	final, err := p.Run()
	if current, ok := final.(model); ok {
		current.lifetime.close()
		current.accountIO.close()
		current.workspaceIO.close()
		current.catalogIO.close()
	}
	return err
}
