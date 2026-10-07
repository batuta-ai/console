package tui

import (
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
	scrActivity screen = "activity"
	scrReview   screen = "review"
)

type model struct {
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
	return model{api: api, screen: scrHome, clientID: clientID(), lifetime: &terminalLifetime{}}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.refresh(), m.tick()) }

func (m model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

type tickMsg struct{}

func (m model) refresh() tea.Cmd {
	return func() tea.Msg {
		var msg dataMsg
		_ = m.api.Get("/v0/workspaces", &msg.ws)
		_ = m.api.Get("/v0/missions", &msg.missions)
		_ = m.api.Get("/v0/squads", &msg.squads)
		_ = m.api.Get("/v0/accounts", &msg.accounts)
		_ = m.api.Get("/v0/activity", &msg.activity)
		_ = m.api.Get("/v0/sessions", &msg.sessions)
		_ = m.api.Get("/v0/limits", &msg.limits)
		if len(msg.ws) > 0 {
			wsID := msg.ws[0].ID
			if m.missionID != "" {
				for _, msn := range msg.missions {
					if msn.ID == m.missionID && msn.WorkspaceID != "" {
						wsID = msn.WorkspaceID
						break
					}
				}
			}
			_ = m.api.Get("/v0/watch?workspace="+wsID, &msg.watch)
		}
		return msg
	}
}

type dataMsg struct {
	ws       []domain.Workspace
	missions []domain.Mission
	squads   []domain.Squad
	accounts []domain.Account
	activity []domain.Event
	sessions []domain.Session
	limits   []domain.LimitSnapshot
	watch    domain.WatchView
}

type statusMsg string

func (m model) applyData(msg dataMsg) model {
	m.ws, m.missions, m.squads, m.accounts, m.activity = msg.ws, msg.missions, msg.squads, msg.accounts, msg.activity
	m.sessions, m.limits, m.watch = msg.sessions, msg.limits, msg.watch
	return m
}

type createdMsg struct {
	q    domain.Squad
	data dataMsg
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case terminalExitRequest:
		return m.exitTerminal(msg.suspend)
	case terminalExitReady:
		m.exitReady = true
		if msg.suspend {
			return m, terminalSuspend()
		}
		return m, tea.Quit
	case terminalResumed:
		m.exiting, m.exitReady, m.termMode = false, false, false
		m.lifetime = &terminalLifetime{}
		m.link = nil
		m.termFrame.disconnected = true
		m.termFrame.owned = false
		m.termFrame.problem = "disconnected after suspend; explicit reconnect required"
		if msg.err != nil {
			m.termFrame.problem = msg.err.Error()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.termMode {
			return m, m.sendResize(msg.Width, msg.Height-10)
		}
	case dataMsg:
		m = m.applyData(msg)
	case tickMsg:
		return m, tea.Batch(m.refresh(), m.tick())
	case statusMsg:
		m.validMsg = string(msg)
	case createdMsg:
		m = m.applyData(msg.data)
		m.editor, m.editIdx, m.validMsg = msg.q, 0, ""
		m.screen = scrEditor
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
			if m.screen == scrEditor {
				m.screen, m.cursor, m.validMsg = scrSquads, 0, ""
				return m, nil
			}
		case "esc":
			if m.termMode {
				return m, m.sendPTY("\x1b")
			}
			if m.screen == scrEditor {
				m.screen, m.cursor, m.validMsg = scrSquads, 0, ""
				return m, nil
			}
		}
		if m.termMode {
			if m.link != nil {
				m.link.offer(terminalAction{key: &msg})
			}
			return m, nil
		}
		if m.screen == scrEditor {
			return m.updateEditor(msg)
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m.exitTerminal(false)
		case "down":
			m.cursor++
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter":
			m = m.open()
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
			if m.screen == scrSquads {
				return m, m.createSquad()
			}
		}
	}
	return m, nil
}

func (m model) updateEditor(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m.exitTerminal(false)
	case "down":
		if m.editIdx < len(m.editor.Members)-1 {
			m.editIdx++
		}
	case "up":
		if m.editIdx > 0 {
			m.editIdx--
		}
	case "n":
		acct := ""
		if len(m.accounts) > 0 {
			acct = m.accounts[0].ID
		}
		m.editor.Members = append(m.editor.Members, domain.Member{
			Role: "Member", CLI: "codex", AccountID: acct, Maestro: len(m.editor.Members) == 0,
		})
		m.editIdx = len(m.editor.Members) - 1
		m.validMsg = ""
	case "x":
		if m.editIdx >= 0 && m.editIdx < len(m.editor.Members) {
			m.editor.Members = append(m.editor.Members[:m.editIdx], m.editor.Members[m.editIdx+1:]...)
			if m.editIdx >= len(m.editor.Members) && m.editIdx > 0 {
				m.editIdx--
			}
		}
		m.validMsg = ""
	case "m":
		for i := range m.editor.Members {
			m.editor.Members[i].Maestro = i == m.editIdx
		}
		m.validMsg = ""
	case "v":
		return m, m.validateEditor()
	case "s":
		return m, m.saveEditor()
	}
	return m, nil
}

func (m model) validateEditor() tea.Cmd {
	ed := m.editor
	return func() tea.Msg {
		id := ed.ID
		if id == "" {
			id = "draft"
		}
		var out struct {
			Validation domain.Validation `json:"validation"`
		}
		if err := m.api.Post("/v0/squads/"+id+"/validate", ed, &out); err != nil {
			return statusMsg(err.Error())
		}
		return statusMsg(out.Validation.Title + ". " + out.Validation.Text)
	}
}

func (m model) saveEditor() tea.Cmd {
	ed := m.editor
	return func() tea.Msg {
		var q domain.Squad
		if err := m.api.Post("/v0/squads", ed, &q); err != nil {
			return statusMsg(err.Error())
		}
		return statusMsg("saved v" + fmt.Sprint(q.Version) + " — catalog write, no model")
	}
}

func (m model) createSquad() tea.Cmd {
	return func() tea.Msg {
		var q domain.Squad
		if err := m.api.Post("/v0/squads", map[string]any{"name": "New squad", "members": []any{}}, &q); err != nil {
			return statusMsg(err.Error())
		}
		var msg dataMsg
		_ = m.api.Get("/v0/workspaces", &msg.ws)
		_ = m.api.Get("/v0/missions", &msg.missions)
		_ = m.api.Get("/v0/squads", &msg.squads)
		_ = m.api.Get("/v0/accounts", &msg.accounts)
		_ = m.api.Get("/v0/activity", &msg.activity)
		_ = m.api.Get("/v0/sessions", &msg.sessions)
		_ = m.api.Get("/v0/limits", &msg.limits)
		return createdMsg{q: q, data: msg}
	}
}

func (m model) open() model {
	switch m.screen {
	case scrHome:
		m.screen = scrMissions
	case scrMissions:
		if m.cursor < len(m.missions) {
			m.missionID = m.missions[m.cursor].ID
		}
		m.screen = scrRoom
	case scrSquads:
		if m.cursor < len(m.squads) {
			m.editor = m.squads[m.cursor]
			m.editIdx, m.validMsg = 0, ""
			m.screen = scrEditor
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
	case scrAccounts:
		m.viewAccounts(&b)
	case scrActivity:
		m.viewActivity(&b)
	case scrReview:
		m.viewReview(&b)
	}
	panel := asciiFold(b.String())
	b.Reset()
	var terminal strings.Builder
	if m.screen == scrRoom {
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
	} else if m.screen == scrEditor {
		fmt.Fprintln(&b, "n add  x remove  m maestro  v validate  s save  esc back")
	} else {
		fmt.Fprintln(&b, i18n.T("keys"))
	}
	return panel + terminal.String() + asciiFold(b.String())
}

func (m model) viewHome(b *strings.Builder) {
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
		fmt.Fprintf(b, "%s%-20s %-12s %s\n", mark, clip(msn.Title, 20), clip(msn.SquadSnapshot.Name, 12), msn.Status)
	}
	if len(m.missions) == 0 {
		fmt.Fprintln(b, "  no missions")
	}
}

func (m model) viewRoom(b *strings.Builder) {
	title := i18n.T("room")
	for _, msn := range m.missions {
		if msn.ID == m.missionID {
			title = msn.Title
			break
		}
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
		for _, msn := range m.missions {
			if msn.ID != m.missionID {
				continue
			}
			for _, mb := range msn.SquadSnapshot.Members {
				fmt.Fprintf(b, "  %-18s %-11s %s\n", clip(mb.Role, 18), mb.CLI, mb.AccountID)
			}
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
		fmt.Fprintf(b, "%s%-20s %s  %d members\n", mark, clip(q.Name, 20), state, len(q.Members))
	}
}

func (m model) viewEditor(b *strings.Builder) {
	fmt.Fprintf(b, "\nSQUAD EDITOR  %s  v%d\n", m.editor.Name, m.editor.Version)
	fmt.Fprintf(b, "%s\n", m.editor.Purpose)
	fmt.Fprintln(b, "ROLE                 CLI          ACCOUNT              MAESTRO")
	if len(m.editor.Members) == 0 {
		fmt.Fprintln(b, "  no members yet")
	}
	for i, mb := range m.editor.Members {
		mark := "  "
		if i == m.editIdx {
			mark = "> "
		}
		ms := "no"
		if mb.Maestro {
			ms = "yes"
		}
		fmt.Fprintf(b, "%s%-20s %-12s %-20s %s\n", mark, clip(mb.Role, 20), clip(mb.CLI, 12), clip(mb.AccountID, 20), ms)
	}
	if m.validMsg != "" {
		fmt.Fprintf(b, "\n%s\n", m.validMsg)
	}
}

func (m model) viewAccounts(b *strings.Builder) {
	fmt.Fprintf(b, "\n%s\n", i18n.T("accounts"))
	fmt.Fprintln(b, "ACCOUNT              ISOLATION           LIMIT                    BAR")
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
	}
	if len(m.accounts) == 0 {
		fmt.Fprintln(b, "  no accounts")
	}
	fmt.Fprintln(b, "connect creates an isolated HOME; official login is the session PTY")
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
		msg := m.refresh()()
		if dm, ok := msg.(dataMsg); ok {
			m = m.applyData(dm)
		}
		_, _ = fmt.Fprint(os.Stdout, m.render())
		return nil
	}
	return runTerminalProgram(m)
}

func runTerminalProgram(m model, options ...tea.ProgramOption) error {
	defer m.lifetime.close()
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
	}
	return err
}
