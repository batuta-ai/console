package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

const journeyPageSize = 8

func journeyByteLines(content []byte) []string {
	var lines []string
	for offset := 0; offset < len(content); {
		end := min(offset+32, len(content))
		if newline := bytes.LastIndexByte(content[offset:end], '\n'); newline >= 0 {
			end = offset + newline + 1
		}
		lines = append(lines, fmt.Sprintf("%08x %s", offset, strconv.QuoteToASCII(string(content[offset:end]))))
		offset = end
	}

	return lines
}
func journeySourceLines(d domain.PlanDetail) []string {
	meta := d
	meta.Content = nil
	meta.Approval = nil
	raw, _ := json.MarshalIndent(meta, "", "  ")
	lines := []string{i18n.T("journey_metadata")}
	// ASCII quoting preserves control bytes and prevents terminal escape execution.
	lines = append(lines, journeyByteLines(raw)...)
	lines = append(lines, i18n.T("journey_bytes"))
	return append(lines, journeyByteLines(d.Content)...)
}
func (m model) editJourney(text string, backspace bool) model {
	r := m.journeyRecord()
	if m.journeyBusy() || r.confirm != "" {
		return m
	}
	switch r.mode {
	case "bind":
		if r.focus == 0 {
			r.path = catalogText(r.path, text, backspace, false)
		}
		if r.focus == 1 {
			r.delivery = catalogText(r.delivery, text, backspace, false)
		}
	case "session_id":
		r.sessionID = catalogText(r.sessionID, text, backspace, false)
	case "receipt_id":
		r.receiptID = catalogText(r.receiptID, text, backspace, false)
	case "answer":
		if r.focus == 0 {
			r.answer = catalogText(r.answer, text, backspace, true)
		}
	}
	m.putJourney(r)
	return m
}
func (m model) updateJourney(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	r := m.journeyRecord()
	key := msg.String()
	if key == "ctrl+c" || key == "ctrl+z" {
		next, cmd := m.exitAccountWork(key == "ctrl+z")
		return next, cmd, true
	}
	if key == "esc" && (r.mode != "" || r.confirm != "") {
		if r.confirm != "" {
			r.confirm = ""
		} else {
			r.mode = ""
		}
		m.putJourney(r)
		return m, m.cancelJourney(), true
	}
	if r.confirm != "" {
		if key == "enter" {
			if r.confirm == "new_answer" {
				if r.fresh && r.nextQuestion != nil && r.state != nil && r.state.Watch != nil && r.state.Watch.PendingQuestion != nil && r.state.Watch.PendingQuestion.Target == r.nextQuestion.Target {
					r.question, r.answer, r.mode, r.focus = r.nextQuestion, "", "answer", 0
				} else {
					r.problem = "journey_question_changed"
					r.fresh = false
				}
				r.nextQuestion = nil
				r.confirm = ""
				m.putJourney(r)
				return m, nil, true
			}
			next, cmd := m.mutateJourney(r.confirm)
			return next, cmd, true
		}
		return m, nil, true
	}
	switch r.mode {
	case "bind", "receipt_id", "session_id", "answer":
		switch key {
		case "tab", "shift+tab":
			n := 3
			if r.mode == "answer" {
				n = 2
			}
			if r.mode == "receipt_id" || r.mode == "session_id" {
				n = 1
			}
			r.focus = (r.focus + 1) % n
		case "enter":
			switch r.mode {
			case "bind":
				if r.focus == 2 && r.path != "" {
					r.confirm = "bind"
				}
			case "session_id":
				m.putJourney(r)
				return m, m.readJourney("sessions"), true
			case "receipt_id":
				m.putJourney(r)
				return m, m.readJourney("receipt"), true
			case "answer":
				if r.focus == 1 {
					r.confirm = "answer"
				} else {
					return m.editJourney("\n", false), nil, true
				}
			}
		default:
			return m.editJourney(msg.Text, key == "backspace"), nil, true
		}
		m.putJourney(r)
		return m, nil, true
	case "source", "receipt", "progress":
		var lines []string
		if r.mode == "progress" {
			lines = journeyProgressLines(r)
		}
		if r.mode == "source" && r.detail != nil {
			lines = journeySourceLines(*r.detail)
		}
		if r.mode == "receipt" && r.historical != nil {
			lines = journeySourceLines(domain.PlanDetail{Identity: r.historical.Identity, Content: r.historical.Content})
		}
		switch key {
		case "down", "pgdown":
			r.page = min(r.page+journeyPageSize, max(0, len(lines)-1))
		case "up", "pgup":
			r.page = max(0, r.page-journeyPageSize)
		case "y":
			if r.mode == "source" && r.detail != nil && r.sourceFresh && !r.nativeSnapshot && r.inspected >= len(lines) {
				r.confirm = "approve"
			}
		case "o":
			m.putJourney(r)
			return m, m.readJourney("source"), true
		default:
			return m, nil, true
		}
		if r.mode == "source" && r.page <= r.inspected {
			r.inspected = max(r.inspected, min(r.page+journeyPageSize, len(lines)))
		}
		m.putJourney(r)
		return m, nil, true
	}
	switch key {
	case "p":
		if r.session == nil && r.uncertain == "" {
			_, err := planningMember(r.mission)
			if err != nil {
				r.problem = "journey_identity"
			} else {
				r.confirm = "planning"
			}
		}
	case "b":
		r.mode = "bind"
		r.focus = 0
	case "o":
		m.putJourney(r)
		return m, m.readJourney("source"), true
	case "w":
		r.mode = "progress"
		r.page = 0
	case "h":
		r.mode = "receipt_id"
		r.focus = 0
	case "k":
		if r.receipt != nil {
			r.historical = r.receipt
			r.mode = "receipt"
			r.page = 0
		} else {
			r.mode = "receipt_id"
			r.focus = 0
		}
	case "f":
		m.putJourney(r)
		return m, m.readJourney("inspect"), true
	case "l":
		m.putJourney(r)
		return m, m.readJourney("sessions"), true
	case "e":
		if r.receipt != nil && r.fresh {
			r.confirm = "start"
		}
	case "g":
		if r.state != nil && r.fresh {
			r.confirm = "resume"
		}
	case "n":
		if r.fresh && r.state != nil && r.state.Watch != nil && r.state.Watch.PendingQuestion != nil {
			q := *r.state.Watch.PendingQuestion
			r.nextQuestion = &q
			r.confirm = "new_answer"
		}
	case "j":
		if r.fresh && r.state != nil && r.state.Watch != nil && r.state.Watch.PendingQuestion != nil {
			if r.question == nil || r.answer == "" {
				q := *r.state.Watch.PendingQuestion
				r.question = &q
			}
			r.mode = "answer"
			r.focus = 0
		}
	default:
		return m, nil, false
	}
	m.putJourney(r)
	return m, nil, true
}
func journeyJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	// JSON's UTF-8 is quoted a second time for lossless ASCII terminal display.
	return strconv.QuoteToASCII(string(b))
}
func (m model) viewJourney(b *strings.Builder) bool {
	if !m.journeyVisible() {
		return false
	}
	r := m.journeyRecord()
	fmt.Fprintln(b, i18n.T("journey_keys"))
	if m.journeyBusy() {
		fmt.Fprintln(b, i18n.T("journey_pending"))
	}
	if !r.fresh {
		fmt.Fprintln(b, i18n.T("journey_stale"))
	}
	if r.uncertain != "" {
		fmt.Fprintln(b, i18n.T("journey_uncertain"), r.uncertain)
		for _, out := range r.outcomes {
			if out.effect != "uncertain" {
				continue
			}
			fmt.Fprintln(b, i18n.T("journey_unconfirmed"))
			if out.session != nil {
				fmt.Fprintln(b, journeyJSON(out.session))
			}
			if out.mission != nil {
				fmt.Fprintln(b, journeyJSON(out.mission))
			}
			if out.execution.Delivery != "" || out.execution.OpenedDigest != "" {
				fmt.Fprintln(b, journeyJSON(out.execution))
			}
		}
	}
	if r.problem != "" {
		fmt.Fprintln(b, i18n.T(r.problem))
		fmt.Fprintln(b, strconv.QuoteToASCII(m.journeyErrorText(r)))
	}
	if r.effect != "" {
		fmt.Fprintf(b, "%s: %s\n", i18n.T("journey_effect"), r.effect)
	}
	if r.confirm != "" {
		fmt.Fprintf(b, "%s: %s\n%s\n", i18n.T("journey_confirm"), i18n.T("journey_"+r.confirm), i18n.T("journey_confirm_keys"))
		switch r.confirm {
		case "planning":
			member, _ := planningMember(r.mission)
			fmt.Fprintln(b, journeyJSON(member))
		case "bind":
			fmt.Fprintf(b, "%s\n%s\n%s\n", r.mission.WorkspaceID, strconv.QuoteToASCII(r.path), strconv.QuoteToASCII(r.delivery))
		case "approve":
			if r.detail != nil {
				fmt.Fprintln(b, journeyJSON(r.detail.Identity))
			}
		case "start", "resume":
			if r.receipt != nil {
				fmt.Fprintln(b, journeyJSON(r.receipt.Identity))
				fmt.Fprintln(b, r.receipt.ID)
			}
			if r.state != nil {
				fmt.Fprintln(b, r.state.Delivery, r.state.OpenedDigest)
			}
		case "new_answer":
			if r.nextQuestion != nil {
				fmt.Fprintln(b, journeyJSON(r.nextQuestion.Target))
				fmt.Fprintln(b, strconv.QuoteToASCII(r.nextQuestion.Prompt))
			}
		case "answer":
			if r.question != nil {
				fmt.Fprintln(b, journeyJSON(r.question.Target))
			}
			fmt.Fprintln(b, strconv.QuoteToASCII(r.answer))
		}
		return true
	}
	switch r.mode {
	case "source", "receipt", "progress":
		var lines []string
		if r.mode == "progress" {
			lines = journeyProgressLines(r)
		}
		if r.mode == "source" && r.detail != nil {
			label := "journey_source"
			if r.nativeSnapshot {
				label = "journey_native_snapshot"
			}
			fmt.Fprintln(b, i18n.T(label), r.detail.Identity.Version)
			if r.nativeSnapshot {
				fmt.Fprintln(b, i18n.T("journey_current_unavailable"))
			}
			lines = journeySourceLines(*r.detail)
		}
		if r.mode == "receipt" && r.historical != nil {
			fmt.Fprintln(b, i18n.T("journey_receipt"), r.historical.ID, r.historical.Identity.Version, r.historical.ApprovedAt)
			lines = journeySourceLines(domain.PlanDetail{Identity: r.historical.Identity, Content: r.historical.Content})
		}
		end := min(len(lines), r.page+journeyPageSize)
		for _, line := range lines[min(r.page, len(lines)):end] {
			fmt.Fprintln(b, line)
		}
		fmt.Fprintf(b, "%d/%d\n", end, len(lines))
		if end == len(lines) {
			fmt.Fprintln(b, i18n.T("journey_end"))
		}
		if r.mode == "progress" || r.nativeSnapshot || r.mode == "receipt" {
			fmt.Fprintln(b, i18n.T("journey_read_keys"))
		} else {
			fmt.Fprintln(b, i18n.T("journey_source_keys"))
		}
		return true
	case "bind":
		fmt.Fprintln(b, i18n.T("journey_bind"), r.mission.WorkspaceID)
		fmt.Fprintln(b, catalogMarker(r.focus == 0)+i18n.T("journey_path")+": "+strconv.QuoteToASCII(r.path))
		fmt.Fprintln(b, catalogMarker(r.focus == 1)+i18n.T("journey_delivery")+": "+strconv.QuoteToASCII(r.delivery))
		fmt.Fprintln(b, catalogMarker(r.focus == 2)+i18n.T("journey_review"))
		fmt.Fprintln(b, i18n.T("journey_form_keys"))
		return true
	case "session_id":
		fmt.Fprintln(b, i18n.T("journey_session_id"), strconv.QuoteToASCII(r.sessionID))
		for _, candidate := range r.sessionCandidates {
			fmt.Fprintln(b, journeyJSON(candidate))
		}
		return true
	case "receipt_id":
		fmt.Fprintln(b, i18n.T("journey_receipt_id"), strconv.QuoteToASCII(r.receiptID))
		return true
	case "answer":
		if r.question != nil {
			fmt.Fprintln(b, journeyJSON(r.question.Target))
			fmt.Fprintln(b, strconv.QuoteToASCII(r.question.Prompt))
		}
		fmt.Fprintln(b, catalogMarker(r.focus == 0)+strconv.QuoteToASCII(r.answer))
		fmt.Fprintln(b, catalogMarker(r.focus == 1)+i18n.T("journey_review"))
		fmt.Fprintln(b, i18n.T("journey_answer_keys"))
		return true
	}
	if r.mission.Binding != nil {
		fmt.Fprintln(b, journeyJSON(r.mission.Binding))
	}
	if r.receipt != nil {
		fmt.Fprintln(b, i18n.T("journey_receipt"), r.receipt.ID, r.receipt.Identity.Version)
	}
	if r.session != nil {
		fmt.Fprintln(b, i18n.T("journey_session"), journeyJSON(r.session))
	}
	if r.state != nil {
		completion := "journey_not_done"
		if r.fresh && r.state.Gate != nil && r.state.Gate.Cleared && r.state.Watch != nil && r.state.Watch.Source == domain.SourceCurrent {
			completion = "journey_done"
		}
		fmt.Fprintln(b, i18n.T(completion))
		fmt.Fprintln(b, i18n.T("journey_execution"), r.state.Delivery, r.state.OpenedDigest)
		fmt.Fprintln(b, journeyJSON(r.state.Gate))
	}
	if r.watch.PendingQuestion != nil {
		fmt.Fprintln(b, journeyJSON(r.watch.PendingQuestion))
	}
	fmt.Fprintf(b, "%s: %s %s %s %s\n", i18n.T("journey_source_identity"), r.watch.MissionID, r.watch.WorkspaceID, r.watch.Delivery, r.watch.Source)
	return false
}

func journeyProgressLines(r journeyRecord) []string {
	raw, _ := json.MarshalIndent(struct {
		Mission   string                 `json:"mission"`
		Workspace string                 `json:"workspace"`
		State     *domain.ExecutionState `json:"execution"`
		Watch     domain.WatchView       `json:"watch"`
	}{r.mission.ID, r.mission.WorkspaceID, r.state, r.watch}, "", "  ")
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		lines[i] = strconv.QuoteToASCII(line)
	}
	return lines
}
