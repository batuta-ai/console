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

func journeyByteLines(content []byte, widths ...int) []string {
	width := 240
	if len(widths) > 0 && widths[0] > 0 {
		width = widths[0]
	}
	return journeyByteRows(content, width, 32)
}

func journeyDataLines(content []byte, width int) []string {
	if width <= 0 {
		width = 240
	}
	return journeyByteRows(content, width, width-11)
}

func journeyByteRows(content []byte, width, chunk int) []string {
	var lines []string
	for offset := 0; offset < len(content); {
		end := min(offset+max(1, chunk), len(content))
		if newline := bytes.LastIndexByte(content[offset:end], '\n'); newline >= 0 {
			end = offset + newline + 1
		}
		for end > offset+1 && 9+len(strconv.QuoteToASCII(string(content[offset:end]))) > width-1 {
			end--
		}
		lines = append(lines, fmt.Sprintf("%08x %s", offset, strconv.QuoteToASCII(string(content[offset:end]))))
		offset = end
	}

	return lines
}
func journeySourceLines(d domain.PlanDetail, widths ...int) []string {
	meta := d
	meta.Content = nil
	meta.Approval = nil
	raw, _ := json.MarshalIndent(meta, "", "  ")
	lines := []string{i18n.T("journey_metadata")}
	// ASCII quoting preserves control bytes and prevents terminal escape execution.
	lines = append(lines, journeyByteLines(raw, widths...)...)
	lines = append(lines, i18n.T("journey_bytes"))
	return append(lines, journeyByteLines(d.Content, widths...)...)
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
		switch key {
		case "up", "pgup", "down", "pgdown":
			r.confirmPage = journeyPage(r.confirmPage, key, len(journeyDataLines(r.confirmData, m.width)), m.journeyBodyRows(r))
			m.putJourney(r)
			return m, nil, true
		}
		if key == "enter" {
			if !bytes.Equal(r.confirmData, journeyConfirmationBytes(r)) {
				if r.confirm == "planning" {
					r.problem = "journey_identity"
					m.putJourney(r)
				}
				return m, nil, true
			}
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
		lines := m.journeyInspectionLines(r)
		switch key {
		case "down", "pgdown":
			r.page = journeyPage(r.page, key, len(lines), m.journeyBodyRows(r))
		case "up", "pgup":
			r.page = journeyPage(r.page, key, len(lines), m.journeyBodyRows(r))
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
			r.inspected = max(r.inspected, min(r.page+m.journeyBodyRows(r), len(lines)))
		}
		m.putJourney(r)
		return m, nil, true
	}
	switch key {
	case "i":
		if pending := m.journey.pending; pending != nil && pending.kind == "source" && pending.key == journeyID(r.mission) {
			return m, nil, true
		}
		return m, nil, false
	case "z":
		if !m.journeyBusy() && !r.stopUnresolved() && r.state != nil && r.state.Active {
			target := domain.ExecutionStopRequest{Target: r.state.Target, ApprovalID: r.state.ApprovalID, Delivery: r.state.Delivery, RunID: r.state.RunID}
			if target.Validate() == nil {
				r.stopSelection = &target
				r.confirm = "stop"
			}
		}
	case "y":
		if !m.journeyBusy() && r.stopUnresolved() {
			target := r.stopOutcome.stop.ExecutionStopRequest
			r.stopSelection = &target
			r.confirm = "stop_observe"
		}
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
func (m model) journeyInspectionVisible() bool {
	if !m.journeyVisible() {
		return false
	}
	r := m.journeyRecord()
	return r.confirm != "" || r.mode == "source" || r.mode == "receipt" || r.mode == "progress"
}

func (m model) viewJourney(b *strings.Builder) bool {
	if !m.journeyVisible() {
		return false
	}
	r := m.journeyRecord()
	if m.journeyInspectionVisible() {
		m.viewJourneyInspection(b, r)
		return true
	}
	fmt.Fprintln(b, i18n.T("journey_keys"))
	fmt.Fprintln(b, i18n.T("journey_inspection_open"))
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
	for _, out := range r.outcomes {
		if out.stop == nil {
			continue
		}
		label := string(out.stop.Effect)
		if out.effect == "not_sent" {
			label = out.effect
		}
		if r.stopOutcome != nil && out.request == r.stopOutcome.request {
			fmt.Fprintln(b, i18n.T("journey_stop_"+label))
		} else {
			fmt.Fprintln(b, i18n.T("journey_stop_history"))
		}
		fmt.Fprintln(b, journeyJSON(out.stop))
		if out.err != nil {
			fmt.Fprintln(b, strconv.QuoteToASCII(out.err.Error()))
		}
	}

	switch r.mode {
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

func journeyProgressLines(r journeyRecord, width int, pending *domain.ExecutionStopRequest) []string {
	type observation struct {
		Latest         bool                         `json:"latest_observation"`
		MatchesCurrent bool                         `json:"matches_current_attempt"`
		Request        *domain.ExecutionStopRequest `json:"request"`
		Result         *domain.ExecutionStopResult  `json:"result"`
		Effect         string                       `json:"transport_effect"`
		Error          string                       `json:"error"`
	}
	stops := []observation{}
	for _, out := range r.outcomes {
		if out.stop == nil {
			continue
		}
		item := observation{Latest: r.stopOutcome != nil && out.request == r.stopOutcome.request, Result: out.stop, Effect: out.effect}
		if out.request != nil {
			item.Request = out.request.stopTarget
		}
		if item.Request == nil {
			target := out.stop.ExecutionStopRequest
			item.Request = &target
		}
		if out.err != nil {
			item.Error = out.err.Error()
		}
		item.MatchesCurrent = r.state != nil && r.state.Target == out.stop.Target && r.state.ApprovalID == out.stop.ApprovalID && r.state.Delivery == out.stop.Delivery && r.state.RunID == out.stop.RunID
		stops = append(stops, item)
	}
	raw, _ := json.MarshalIndent(struct {
		Mission   string                       `json:"mission"`
		Workspace string                       `json:"workspace"`
		State     *domain.ExecutionState       `json:"execution"`
		Watch     domain.WatchView             `json:"watch"`
		Fresh     bool                         `json:"fresh"`
		Uncertain string                       `json:"unresolved_mutation"`
		Problem   string                       `json:"problem"`
		Error     string                       `json:"problem_error"`
		Pending   *domain.ExecutionStopRequest `json:"pending_stop"`
		Stops     []observation                `json:"stops"`
	}{r.mission.ID, r.mission.WorkspaceID, r.state, r.watch, r.fresh, r.uncertain, r.problem, model{}.journeyErrorText(r), pending, stops}, "", "  ")
	return journeyDataLines(raw, width)
}

func journeyConfirmationBytes(r journeyRecord) []byte {
	var value any
	switch r.confirm {
	case "stop", "stop_observe":
		value = r.stopSelection
	case "planning":
		value, _ = planningMember(r.mission)
	case "bind":
		value = struct {
			Workspace string `json:"workspace_id"`
			Path      string `json:"plan_path"`
			Delivery  string `json:"delivery_id"`
		}{r.mission.WorkspaceID, r.path, r.delivery}
	case "approve":
		if r.detail != nil {
			value = r.detail.Identity
		}
	case "start":
		if r.receipt != nil {
			value = domain.ExecutionRequest{Target: r.receipt.Identity, ApprovalID: r.receipt.ID}
		}
	case "resume":
		if r.receipt != nil && r.state != nil {
			value = domain.ResumeRequest{Target: r.receipt.Identity, ApprovalID: r.receipt.ID, Delivery: r.state.Delivery, OpenedDigest: r.state.OpenedDigest}
		}
	case "new_answer":
		if r.nextQuestion != nil {
			value = struct {
				domain.QuestionIdentity
				Prompt string `json:"prompt"`
			}{r.nextQuestion.Target, r.nextQuestion.Prompt}
		}
	case "answer":
		if r.question != nil {
			value = r.question.Target.Answer(r.answer)
		}
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	return raw
}

func journeyPage(page int, key string, length, size int) int {
	switch key {
	case "up", "pgup":
		return max(0, page-size)
	case "down", "pgdown":
		if page+size < length {
			return page + size
		}
	}
	return page
}

func (m model) journeyBodyRows(r journeyRecord) int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	// Reserve the page indicator, end marker, controls and outer footer.
	header, _ := m.journeyInspectionHeader(r)
	return max(0, min(journeyPageSize, height-strings.Count(header, "\n")-5))
}

func (m model) journeyInspectionLines(r journeyRecord) []string {
	switch r.mode {
	case "source":
		if r.detail != nil {
			return journeySourceLines(*r.detail, m.width)
		}
	case "receipt":
		if r.historical != nil {
			raw, _ := json.MarshalIndent(r.historical, "", "  ")
			return journeyByteLines(raw, m.width)
		}
	case "progress":
		var pending *domain.ExecutionStopRequest
		if req := m.journey.pending; req != nil && req.key == journeyID(r.mission) {
			pending = req.stopTarget
		}
		return journeyProgressLines(r, m.width, pending)
	}
	return nil
}

func (m model) journeyInspectionHeader(r journeyRecord) (string, string) {
	var b strings.Builder
	title, keys := "journey_progress_title", "journey_inspection_read_keys"
	if r.confirm != "" {
		title, keys = "journey_"+r.confirm, "journey_inspection_confirm_keys"
		fmt.Fprint(&b, i18n.T("journey_confirm"), ": ")
	} else if r.mode == "source" {
		title = "journey_source"
		keys = "journey_inspection_source_keys"
		if r.nativeSnapshot {
			title, keys = "journey_native_snapshot", "journey_inspection_read_keys"
		}
	} else if r.mode == "receipt" {
		title = "journey_receipt"
	}
	fmt.Fprintln(&b, i18n.T(title))
	fmt.Fprintf(&b, "mission=%s workspace=%s\n", strconv.QuoteToASCII(r.mission.ID), strconv.QuoteToASCII(r.mission.WorkspaceID))
	if r.confirm != "" && !bytes.Equal(r.confirmData, journeyConfirmationBytes(r)) {
		fmt.Fprintln(&b, i18n.T("journey_inspection_changed"))
	} else {
		fmt.Fprintln(&b, i18n.T("journey_inspection_readonly"))
	}
	if r.problem != "" {
		fmt.Fprintln(&b, i18n.T(r.problem))
		problem := strconv.QuoteToASCII(m.journeyErrorText(r))
		if m.width <= 0 || len(problem) < m.width {
			fmt.Fprintln(&b, problem)
		}
	}
	if r.nativeSnapshot && r.mode == "source" && r.confirm == "" {
		fmt.Fprintln(&b, i18n.T("journey_current_unavailable"))
	}
	if r.mode == "source" && r.confirm == "" && r.detail != nil {
		fmt.Fprintln(&b, r.detail.Identity.Version)
	}
	width := m.width
	if width <= 0 {
		width = 240
	}
	var wrapped strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
		runes := []rune(line)
		for len(runes) > max(1, width-1) {
			fmt.Fprintln(&wrapped, string(runes[:max(1, width-1)]))
			runes = runes[max(1, width-1):]
		}
		fmt.Fprintln(&wrapped, string(runes))
	}
	return wrapped.String(), keys
}

func (m model) viewJourneyInspection(b *strings.Builder, r journeyRecord) {
	b.Reset()
	header, keys := m.journeyInspectionHeader(r)
	b.WriteString(header)
	page, lines := r.page, m.journeyInspectionLines(r)
	if r.confirm != "" {
		page, lines = r.confirmPage, journeyDataLines(r.confirmData, m.width)
	}
	end := min(len(lines), page+m.journeyBodyRows(r))
	for _, line := range lines[min(page, len(lines)):end] {
		fmt.Fprintln(b, line)
	}
	fmt.Fprintf(b, "%s %d-%d/%d\n", i18n.T("journey_inspection_rows"), min(page+1, len(lines)), end, len(lines))
	if end == len(lines) {
		fmt.Fprintln(b, i18n.T("journey_end"))
	}
	fmt.Fprintln(b, i18n.T(keys))
}
