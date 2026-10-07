package tui

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

const scrMissionForm screen = "mission_form"

type missionForm struct {
	title, brief, workspaceID string
	squad                     domain.Squad
	reviewing                 bool
}

func (f missionForm) request() client.CreateMissionRequest {
	return client.CreateMissionRequest{WorkspaceID: f.workspaceID, Title: f.title, Brief: f.brief, SquadID: f.squad.ID}
}
func (m model) startSquad(q domain.Squad) model {
	if m.catalog.pending != nil {
		return m
	}
	m.catalog.epoch++
	m.catalog.revision++
	m.catalog.form++
	m.catalog.focus = 0
	m.catalog.validation = nil
	m.catalog.notice = ""
	m.editor = cloneSquad(q)
	m.editIdx = 0
	m.screen = scrEditor
	return m
}
func (m model) catalogEdited() model {
	m.catalog.revision++
	m.catalog.validation = nil
	m.catalog.mission.reviewing = false
	m.catalog.notice = ""
	if m.catalog.uncertain == nil {
		m.catalog.problem, m.catalog.detail = "", ""
	}
	return m
}
func (m model) catalogEditable() bool {
	return !m.exiting && (m.catalog.pending == nil || m.catalog.pending.kind == "validate")
}
func catalogText(old, text string, backspace, multiline bool) string {
	if backspace {
		r := []rune(old)
		if len(r) > 0 {
			return string(r[:len(r)-1])
		}
		return old
	}
	return old + strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !(multiline && r == '\n') {
			return -1
		}
		return r
	}, text)
}
func (m model) editCatalogText(text string, backspace bool) model {
	if !m.catalogEditable() {
		return m
	}
	before := m
	if m.screen == scrMissionForm {
		if m.catalog.mission.reviewing {
			return m
		}
		switch m.catalog.focus {
		case 0:
			m.catalog.mission.title = catalogText(m.catalog.mission.title, text, backspace, false)
		case 1:
			m.catalog.mission.brief = catalogText(m.catalog.mission.brief, text, backspace, true)
		default:
			return m
		}
	} else {
		m.editor = cloneSquad(m.editor)
		switch m.catalog.focus {
		case 0:
			m.editor.Name = catalogText(m.editor.Name, text, backspace, false)
		case 1:
			m.editor.Purpose = catalogText(m.editor.Purpose, text, backspace, true)
		default:
			if m.editIdx < 0 || m.editIdx >= len(m.editor.Members) {
				return m
			}
			mb := &m.editor.Members[m.editIdx]
			switch m.catalog.focus {
			case 2:
				mb.Role = catalogText(mb.Role, text, backspace, false)
			case 5:
				mb.Model = catalogText(mb.Model, text, backspace, false)
			case 6:
				mb.Effort = catalogText(mb.Effort, text, backspace, false)
			case 7:
				old := strings.Join(mb.Skills, ",")
				value := catalogText(old, text, backspace, false)
				if value == old {
					return m
				}
				if value == "" {
					mb.Skills = nil
				} else {
					mb.Skills = strings.Split(value, ",")
				}
			default:
				return m
			}
		}
	}
	if reflect.DeepEqual(before.editor, m.editor) && reflect.DeepEqual(before.catalog.mission, m.catalog.mission) {
		return m
	}
	return m.catalogEdited()
}
func cycleCatalogID(ids []string, current string, back bool) string {
	// Empty is an explicit option; a recipient is chosen only on a selection key.
	choices := append([]string{""}, ids...)
	index := 0
	for i, id := range choices {
		if id == current {
			index = i
			break
		}
	}
	step := 1
	if back {
		step = len(choices) - 1
	}
	return choices[(index+step)%len(choices)]
}
func (m model) updateEditor(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m.exitAccountWork(false)
	case "esc", "ctrl+]":
		m.screen = scrSquads
		m.cursor = 0
		m.catalog.epoch++
		m.catalog.validation = nil
		return m, m.cancelCatalogRequests()
	case "ctrl+f":
		return m, m.inspectCatalog()
	case "ctrl+d":
		return m.acknowledgeCatalog(), nil
	case "ctrl+s":
		return m.catalogMutation("save")
	case "ctrl+v":
		return m.catalogMutation("validate")
	}
	if !m.catalogEditable() {
		return m, nil
	}
	switch key {
	case "tab":
		m.catalog.focus = (m.catalog.focus + 1) % 9
	case "shift+tab":
		m.catalog.focus = (m.catalog.focus + 8) % 9
	case "ctrl+n":
		m.editor = cloneSquad(m.editor)
		m.editor.Members = append(m.editor.Members, domain.Member{})
		m.editIdx = len(m.editor.Members) - 1
		m.catalog.focus = 2
		m = m.catalogEdited()
	case "ctrl+x":
		if m.editIdx >= 0 && m.editIdx < len(m.editor.Members) {
			m.editor = cloneSquad(m.editor)
			m.editor.Members = append(m.editor.Members[:m.editIdx], m.editor.Members[m.editIdx+1:]...)
			m.editIdx = max(0, min(m.editIdx, len(m.editor.Members)-1))
			m = m.catalogEdited()
		}
	case "ctrl+up":
		if m.editIdx > 0 {
			m.editIdx--
			m = m.catalogEdited()
		}
	case "ctrl+down":
		if m.editIdx+1 < len(m.editor.Members) {
			m.editIdx++
			m = m.catalogEdited()
		}
	case "left", "right", " ", "space":
		if m.catalog.focus != 3 && m.catalog.focus != 4 && m.catalog.focus != 8 {
			return m.editCatalogText(msg.Text, false), nil
		}
		if m.editIdx < 0 || m.editIdx >= len(m.editor.Members) {
			return m, nil
		}
		before := m.editor
		m.editor = cloneSquad(m.editor)
		mb := &m.editor.Members[m.editIdx]
		switch m.catalog.focus {
		case 3:
			mb.CLI = cycleCatalogID([]string{"codex", "claude"}, mb.CLI, key == "left")
			// Retain the selected account only while it matches the new provider.
			match := false
			for _, a := range m.accounts {
				if a.ID == mb.AccountID && a.Adapter == mb.CLI {
					match = true
				}
			}
			if !match {
				mb.AccountID = ""
			}
		case 4:
			var ids []string
			for _, a := range m.accounts {
				if a.Adapter == mb.CLI {
					ids = append(ids, a.ID)
				}
			}
			mb.AccountID = cycleCatalogID(ids, mb.AccountID, key == "left")
		case 8:
			mb.Maestro = !mb.Maestro
		}
		if !reflect.DeepEqual(before, m.editor) {
			m = m.catalogEdited()
		}
	case "enter":
		if m.catalog.focus == 1 {
			m = m.editCatalogText("\n", false)
		}
	default:
		m = m.editCatalogText(msg.Text, key == "backspace")
	}
	return m, nil
}
func (m model) viewEditor(b *strings.Builder) {
	fmt.Fprintf(b, "\n%s  %s  v%d  %s\n", i18n.T("catalog_editor"), m.editor.ID, m.editor.Version, m.editor.Origin)
	if m.editor.ID == "" {
		fmt.Fprintln(b, i18n.T("catalog_local"))
	} else {
		fmt.Fprintf(b, "%s: %t\n", i18n.T("catalog_structural_draft"), m.editor.Draft)
	}
	keys := []string{"catalog_name", "catalog_purpose", "catalog_role", "catalog_cli", "catalog_account", "catalog_model", "catalog_effort", "catalog_skills", "catalog_maestro"}
	values := []string{m.editor.Name, m.editor.Purpose, "", "", "", "", "", "", ""}
	if m.editIdx >= 0 && m.editIdx < len(m.editor.Members) {
		mb := m.editor.Members[m.editIdx]
		values[2] = mb.Role
		values[3] = mb.CLI
		values[4] = mb.AccountID
		values[5] = mb.Model
		values[6] = mb.Effort
		values[7] = strings.Join(mb.Skills, ",")
		values[8] = accountBool(mb.Maestro)
	}
	for i, k := range keys {
		value := values[i]
		if (i == 5 || i == 6) && value == "" {
			value = i18n.T("catalog_inherit")
		}
		fmt.Fprintf(b, "%s%s: %s\n", catalogMarker(i == m.catalog.focus), i18n.T(k), value)
	}
	for i, mb := range m.editor.Members {
		fmt.Fprintf(b, "%s%s  %s  %s  %s\n", catalogMarker(i == m.editIdx), mb.ID, mb.Role, mb.CLI, mb.AccountID)
	}
	if m.catalog.focus == 4 && m.editIdx < len(m.editor.Members) {
		for _, a := range m.accounts {
			if a.Adapter == m.editor.Members[m.editIdx].CLI {
				fmt.Fprintf(b, "  %s %s %s\n", a.ID, a.Label, a.Adapter)
			}
		}
	}
	if v := m.catalog.validation; v != nil {
		fmt.Fprintf(b, "%s: %t  %s  %s\n%s\n", i18n.T("catalog_readiness"), v.Ready, v.Code, v.Title, v.Text)
	} else {
		fmt.Fprintln(b, i18n.T("catalog_validation_unknown"))
	}
}
func catalogMarker(selected bool) string {
	if selected {
		return "> "
	}
	return "  "
}
func (m model) startMission() model {
	if m.catalog.pending != nil {
		return m
	}
	m.catalog.epoch++
	m.catalog.revision++
	m.catalog.form++
	m.catalog.focus = 0
	m.catalog.validation = nil
	m.catalog.notice = ""
	m.catalog.mission = missionForm{}
	if !m.workspace.form && !m.workspace.missing {
		m.catalog.mission.workspaceID = m.workspace.selected.ID
	}
	m.screen = scrMissionForm
	return m
}
func (m model) missionValid() bool {
	f := m.catalog.mission
	if strings.TrimSpace(f.title) == "" || f.workspaceID == "" || f.squad.ID == "" {
		return false
	}
	workspace, squad := false, false
	for _, w := range m.ws {
		if w.ID == f.workspaceID {
			workspace = true
		}
	}
	for _, q := range m.squads {
		if q.ID == f.squad.ID {
			squad = true
		}
	}
	return workspace && squad
}
func (m model) updateMissionForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m.exitAccountWork(false)
	case "esc", "ctrl+]":
		if m.catalog.mission.reviewing && m.catalog.pending == nil {
			m.catalog.mission.reviewing = false
			return m, nil
		}
		m.screen = scrMissions
		m.cursor = 0
		m.catalog.epoch++
		return m, m.cancelCatalogRequests()
	case "ctrl+f":
		return m, m.inspectCatalog()
	case "ctrl+d":
		return m.acknowledgeCatalog(), nil
	}
	if !m.catalogEditable() {
		return m, nil
	}
	if key == "ctrl+enter" || (key == "enter" && (m.catalog.mission.reviewing || m.catalog.focus == 2 || m.catalog.focus == 3)) {
		if !m.missionValid() {
			m.catalog.problem = "catalog_objective_required"
			return m, nil
		}
		if m.catalog.mission.reviewing {
			return m.catalogMutation("create")
		}
		m.catalog.mission.reviewing = true
		m.catalog.problem = ""
		return m, nil
	}
	if m.catalog.mission.reviewing {
		return m, nil
	}
	switch key {
	case "tab":
		m.catalog.focus = (m.catalog.focus + 1) % 4
	case "shift+tab":
		m.catalog.focus = (m.catalog.focus + 3) % 4
	case "left", "right":
		before := m.catalog.mission
		f := &m.catalog.mission
		if m.catalog.focus == 2 {
			var ids []string
			for _, w := range m.ws {
				ids = append(ids, w.ID)
			}
			f.workspaceID = cycleCatalogID(ids, f.workspaceID, key == "left")
		}
		if m.catalog.focus == 3 {
			var ids []string
			for _, q := range m.squads {
				ids = append(ids, q.ID)
			}
			id := cycleCatalogID(ids, f.squad.ID, key == "left")
			f.squad = domain.Squad{}
			for _, q := range m.squads {
				if q.ID == id {
					f.squad = cloneSquad(q)
				}
			}
		}
		if !reflect.DeepEqual(before, m.catalog.mission) {
			m = m.catalogEdited()
		}
	case "enter":
		if m.catalog.focus == 1 {
			m = m.editCatalogText("\n", false)
		}
	default:
		m = m.editCatalogText(msg.Text, key == "backspace")
	}
	return m, nil
}
func (m model) selectMission(msn domain.Mission) model {
	m.catalog.selected = cloneMission(msn)
	m.missionID = msn.ID
	m.workspace.epoch++
	m.workspace.selected = domain.Workspace{ID: msn.WorkspaceID}
	m.workspace.missing = true
	m.workspace.form = false
	for _, w := range m.ws {
		if w.ID == msn.WorkspaceID {
			m.workspace.selected = w
			m.workspace.missing = false
			break
		}
	}
	m.watch = domain.WatchView{}
	m.screen = scrRoom
	return m
}
func (m model) viewMissionForm(b *strings.Builder) {
	f := m.catalog.mission
	fmt.Fprintln(b, i18n.T("catalog_new_mission"))
	for i, row := range []struct{ key, value string }{{"catalog_objective", f.title}, {"catalog_brief", f.brief}, {"catalog_workspace", f.workspaceID}, {"catalog_squad", fmt.Sprintf("%s %s v%d", f.squad.ID, f.squad.Name, f.squad.Version)}} {
		fmt.Fprintf(b, "%s%s: %s\n", catalogMarker(i == m.catalog.focus), i18n.T(row.key), row.value)
	}
	if m.catalog.focus == 2 {
		for _, w := range m.ws {
			fmt.Fprintf(b, "  %s %s %s\n", w.ID, w.Name, w.Root)
		}
	}
	if m.catalog.focus == 3 {
		for _, q := range m.squads {
			fmt.Fprintf(b, "  %s %s v%d\n", q.ID, q.Name, q.Version)
		}
	}
	if f.reviewing {
		fmt.Fprintln(b, i18n.T("catalog_confirm_mission"))
		fmt.Fprintln(b, i18n.T("catalog_snapshot_review"))
	}
}
func (m model) roomMission() domain.Mission {
	if m.catalog.selected.ID == m.missionID {
		return m.catalog.selected
	}
	for _, candidate := range m.missions {
		if candidate.ID == m.missionID {
			return candidate
		}
	}
	return domain.Mission{}
}
func (m model) viewMissionIdentity(b *strings.Builder) {
	msn := m.roomMission()
	if msn.ID == "" || msn.ID != m.missionID {
		return
	}
	fmt.Fprintf(b, "%s  %s: %s  %s: %s v%d  %s\n%s\n%s\n", msn.ID, i18n.T("catalog_workspace"), msn.WorkspaceID, i18n.T("catalog_snapshot"), msn.SquadSnapshot.ID, msn.SquadSnapshot.Version, msn.Status, msn.Title, msn.Brief)
	for _, mb := range msn.SquadSnapshot.Members {
		fmt.Fprintf(b, "%s %s %s %s %s %s %v %t\n", mb.ID, mb.Role, mb.CLI, mb.AccountID, mb.Model, mb.Effort, mb.Skills, mb.Maestro)
	}
}
