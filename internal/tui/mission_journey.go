package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

const journeyTimeout = 15 * time.Second

type journeyRecord struct {
	mission                           domain.Mission
	detail                            *domain.PlanDetail
	receipt                           *domain.PlanApproval
	historical                        *domain.PlanApproval
	sourceFresh, nativeSnapshot       bool
	answerInspected                   bool
	answered                          *domain.QuestionIdentity
	state                             *domain.ExecutionState
	watch                             domain.WatchView
	session                           *domain.Session
	sessionID                         string
	sessionCandidates                 []domain.Session
	outcomes                          []journeyResult
	uncertain                         string
	fresh                             bool
	mode, confirm, problem, effect    string
	path, delivery, receiptID, answer string
	question                          *domain.PendingQuestion
	nextQuestion                      *domain.PendingQuestion
	focus, page, inspected            int
}
type journeyFlow struct {
	epoch   uint64
	records map[string]journeyRecord
	pending *journeyRequest
}
type journeyRequest struct {
	kind, key string
	epoch     uint64
	mission   domain.Mission
	ctx       context.Context
	cancel    context.CancelFunc
	lifetime  *terminalLifetime
	once      sync.Once
	execute   func(context.Context) journeyResult
	result    journeyResult
}
type journeyResult struct {
	request   *journeyRequest
	detail    *domain.PlanDetail
	receipt   *domain.PlanApproval
	state     *domain.ExecutionState
	watch     *domain.WatchView
	session   *domain.Session
	sessions  []domain.Session
	mission   *domain.Mission
	approval  domain.PlanApprovalResult
	execution domain.ExecutionResult
	answer    domain.AnswerResult
	effect    string
	err       error
}

func journeyID(m domain.Mission) string { return m.ID + "\x00" + m.WorkspaceID }
func (m model) journeyRecord() journeyRecord {
	msn := m.roomMission()
	if r, ok := m.journey.records[journeyID(msn)]; ok {
		return r
	}
	return journeyRecord{mission: cloneMission(msn)}
}
func (m *model) saveJourney(key string, record journeyRecord) {
	next := make(map[string]journeyRecord, len(m.journey.records)+1)
	for k, v := range m.journey.records {
		next[k] = v
	}
	next[key] = record
	m.journey.records = next
}
func (m *model) putJourney(r journeyRecord) { m.saveJourney(journeyID(r.mission), r) }
func (m model) journeyBusy() bool           { return m.journey.pending != nil }
func (m model) journeyVisible() bool {
	return (m.screen == scrRoom || m.screen == scrReview) && m.roomMission().ID != ""
}
func (r *journeyRequest) run() tea.Msg {
	r.once.Do(func() {
		defer r.cancel()
		parent, finish, ok := r.lifetime.begin()
		if !ok {
			r.result = journeyResult{effect: "rejected", err: context.Canceled}
		} else {
			defer finish()
			stop := context.AfterFunc(parent, r.cancel)
			defer stop()
			if err := r.ctx.Err(); err != nil {
				r.result = journeyResult{effect: "rejected", err: err}
			} else {
				r.result = r.execute(r.ctx)
			}
		}
		r.result.request = r
	})
	return r.result
}
func (m *model) journeyRequest(kind string, execute func(context.Context) journeyResult) tea.Cmd {
	if m.api == nil || m.exiting || m.journey.pending != nil {
		return nil
	}
	if m.journeyIO == nil {
		m.journeyIO = &terminalLifetime{}
	}
	r := m.journeyRecord()
	if r.mission.ID == "" || r.mission.WorkspaceID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), journeyTimeout)
	req := &journeyRequest{kind: kind, key: journeyID(r.mission), epoch: m.journey.epoch, mission: cloneMission(r.mission), ctx: ctx, cancel: cancel, lifetime: m.journeyIO, execute: execute}
	m.journey.pending = req
	return req.run
}
func (m *model) cancelJourney() tea.Cmd {
	m.journey.epoch++
	for k, r := range m.journey.records {
		r.fresh = false
		r.sourceFresh = false
		r.answerInspected = false
		m.saveJourney(k, r)
	}
	if r := m.journey.pending; r != nil {
		r.cancel()
		return r.run
	}
	return nil
}
func journeyWatchMatches(w domain.WatchView, msn domain.Mission, delivery string) bool {
	return w.MissionID == msn.ID && w.WorkspaceID == msn.WorkspaceID && w.Delivery == delivery
}
func journeyStateMatches(s domain.ExecutionState, receipt domain.PlanApproval, previous *domain.ExecutionState) bool {
	return s.Target == receipt.Identity && s.ApprovalID == receipt.ID && (previous == nil || previous.OpenedDigest == "" || previous.OpenedDigest == s.OpenedDigest)
}
func (m *model) readJourney(kind string) tea.Cmd {
	if !m.journeyVisible() {
		return nil
	}
	r := m.journeyRecord()
	api := m.api
	return m.journeyRequest(kind, func(ctx context.Context) journeyResult {
		out := journeyResult{effect: "read_only"}
		switch kind {
		case "source":
			return readJourneySource(ctx, api, r)
		case "receipt":
			a, err := api.PlanApproval(ctx, r.mission.ID, r.mission.WorkspaceID, r.receiptID)
			out.err = err
			if err == nil {
				out.receipt = &a
			}
		case "sessions":
			sessions, err := api.ListSessions(ctx)
			out.err = err
			if err != nil {
				return out
			}
			member, err := planningMember(r.mission)
			if err != nil {
				out.err = err
				return out
			}
			for _, session := range sessions {
				if session.MissionID != r.mission.ID || session.WorkspaceID != r.mission.WorkspaceID || session.MemberID != member.ID || session.AccountID != member.AccountID || session.Adapter != member.CLI || !session.BatutaInjected {
					continue
				}
				if r.session == nil && (session.Owner != "tui" || session.OwnerClient != m.clientID) {
					continue
				}
				out.sessions = append(out.sessions, session)
				if session.ID == r.sessionID && session.ID != "" {
					selected := session
					out.session = &selected
				}
			}
			if r.sessionID != "" && out.session == nil {
				out.err = domain.ErrInvalidReference
			}
		default:
			if kind == "inspect" && r.uncertain == "bind" {
				missions, err := api.ListMissions(ctx, r.mission.WorkspaceID)
				if err != nil {
					out.err = err
					return out
				}
				for _, candidate := range missions {
					if candidate.ID != r.mission.ID {
						continue
					}
					if out.mission != nil || !journeyBindingMatches(candidate, r) {
						out.err = domain.ErrInvalidReference
						return out
					}
					bound := cloneMission(candidate)
					out.mission = &bound
				}
				if out.mission == nil {
					out.err = domain.ErrInvalidReference
					return out
				}
				return out
			}
			if r.receipt == nil && r.mission.Binding != nil {
				out = readJourneySource(ctx, api, r)
				if out.err != nil {
					return out
				}
				if out.detail.Approval != nil {
					r.receipt = out.detail.Approval
				}
			}
			delivery := ""
			if r.mission.Binding != nil {
				delivery = r.mission.Binding.DeliveryID
			}
			if r.receipt != nil {
				state, err := api.ExecutionState(ctx, r.mission.ID, r.mission.WorkspaceID, r.receipt.ID)
				if err != nil {
					out.err = err
					return out
				}
				if !journeyStateMatches(state, *r.receipt, r.state) {
					out.err = domain.ErrInvalidReference
					return out
				}
				out.state = &state
				if state.OpenedDigest != "" {
					delivery = state.Delivery
				}
			}
			w, err := api.MissionWatch(ctx, r.mission.ID, r.mission.WorkspaceID, delivery)
			out.err = err
			if err == nil && !journeyWatchMatches(w, r.mission, delivery) {
				out.err = domain.ErrInvalidReference
			}
			if out.err == nil {
				out.watch = &w
			}
		}
		return out
	})
}
func readJourneySource(ctx context.Context, api *client.Client, r journeyRecord) journeyResult {
	out := journeyResult{effect: "read_only"}
	d, err := api.PlanDetail(ctx, r.mission.ID, r.mission.WorkspaceID)
	if err != nil {
		out.err = err
		return out
	}
	if r.mission.Binding != nil {
		got, want := d.Identity.Binding, *r.mission.Binding
		got.DeliveryID, got.OpenedDigest = "", ""
		want.DeliveryID, want.OpenedDigest = "", ""
		if got != want {
			out.err = domain.ErrInvalidReference
			return out
		}
	}
	if d.Approval != nil {
		state, err := api.ExecutionState(ctx, r.mission.ID, r.mission.WorkspaceID, d.Approval.ID)
		if err != nil {
			out.err = err
			return out
		}
		if !journeyStateMatches(state, *d.Approval, r.state) || (r.state != nil && r.state.OpenedDigest != "" && (r.receipt == nil || d.Approval.ID != r.receipt.ID)) {
			out.err = domain.ErrInvalidReference
			return out
		}
		out.state = &state
	}
	if r.mission.Binding != nil && r.mission.Binding.DeliveryID != "" && (out.state == nil || !out.state.Bound || out.state.Delivery != r.mission.Binding.DeliveryID || out.state.OpenedDigest != r.mission.Binding.OpenedDigest) {
		out.err = domain.ErrInvalidReference
		return out
	}
	if r.state != nil && r.state.OpenedDigest != "" && (out.state == nil || !out.state.Bound) {
		out.err = domain.ErrInvalidReference
		return out
	}
	out.detail = &d
	return out
}

func planningMember(msn domain.Mission) (domain.Member, error) {
	var lead domain.Member
	count := 0
	for _, mb := range msn.SquadSnapshot.Members {
		if mb.Maestro {
			lead = mb
			count++
		}
	}
	if count != 1 || lead.ID == "" {
		return lead, domain.ErrInvalidReference
	}
	lead, err := domain.SelectSnapshotMember(msn.SquadSnapshot, lead.ID, true)
	if err != nil || lead.AccountID == "" || (lead.CLI != "codex" && lead.CLI != "claude") {
		return lead, domain.ErrInvalidReference
	}
	return lead, nil
}
func journeyBindingMatches(bound domain.Mission, r journeyRecord) bool {
	b := bound.Binding
	return bound.ID == r.mission.ID && bound.WorkspaceID == r.mission.WorkspaceID && reflect.DeepEqual(bound.SquadSnapshot, r.mission.SquadSnapshot) && b != nil && b.WorkspaceID == r.mission.WorkspaceID && b.PlanPath == r.path && b.WorkspaceRoot != "" && b.CanonicalRoot != "" && b.PlanSlug != "" && b.PlanDigest != "" && b.DeliveryID == r.delivery && (b.OpenedDigest == "") == (b.DeliveryID == "")
}

func journeyOperationEffect(err error) string {
	if err == nil {
		return "recorded"
	}
	var op *client.OperationError
	if errors.As(err, &op) && (op.Effect == client.EffectNotDispatched || op.Effect == client.EffectRejected) {
		return "rejected"
	}
	return "uncertain"
}
func (m model) mutateJourney(kind string) (model, tea.Cmd) {
	r := m.journeyRecord()
	if m.api == nil || m.exiting || m.journeyBusy() || (r.uncertain != "" && !(kind == "resume" && r.uncertain == "answer" && r.answerInspected)) || m.catalogBusy() || m.account.pending != nil || m.workspace.pending != nil || m.account.uncertain != nil || m.workspace.uncertain != nil {
		return m, nil
	}
	api, clientID := m.api, m.clientID
	var execute func(context.Context) journeyResult
	switch kind {
	case "planning":
		if r.session != nil {
			return m, nil
		}
		member, err := planningMember(r.mission)
		if err != nil {
			r.problem = "journey_identity"
			m.putJourney(r)
			return m, nil
		}
		if clientID == "" {
			return m, nil
		}
		execute = func(ctx context.Context) journeyResult {
			inject := true
			session, err := api.StartSession(ctx, client.StartSessionRequest{MissionID: r.mission.ID, MemberID: member.ID, AccountID: member.AccountID, Adapter: member.CLI, Inject: &inject, Owner: "tui", Client: clientID})
			out := journeyResult{session: &session, err: err, effect: journeyOperationEffect(err)}
			if err == nil && (session.ID == "" || session.MissionID != r.mission.ID || session.WorkspaceID != r.mission.WorkspaceID || session.MemberID != member.ID || session.AccountID != member.AccountID || session.Adapter != member.CLI || session.Owner != "tui" || session.OwnerClient != clientID || !session.BatutaInjected) {
				out.err = domain.ErrInvalidReference
				out.effect = "uncertain"
			}
			return out
		}
	case "bind":
		if r.nativeSnapshot || (r.state != nil && r.state.OpenedDigest != "") || r.path == "" {
			return m, nil
		}
		execute = func(ctx context.Context) journeyResult {
			bound, err := api.BindMission(ctx, r.mission.ID, client.MissionBindingRequest{WorkspaceID: r.mission.WorkspaceID, PlanPath: r.path, DeliveryID: r.delivery})
			out := journeyResult{mission: &bound, err: err, effect: journeyOperationEffect(err)}
			if err == nil && !journeyBindingMatches(bound, r) {
				out.err = domain.ErrInvalidReference
				out.effect = "uncertain"
			}
			return out
		}
	case "approve":
		if !r.sourceFresh || r.nativeSnapshot || r.detail == nil || r.mode != "source" || r.inspected < len(journeySourceLines(*r.detail)) || r.detail.Identity.Binding.DeliveryID != "" {
			return m, nil
		}
		target := r.detail.Identity
		execute = func(ctx context.Context) journeyResult {
			out, err := api.ApprovePlan(ctx, target)
			return journeyResult{approval: out, receipt: out.Receipt, effect: string(out.Effect), err: err}
		}
	case "start", "resume":
		if !r.fresh || r.receipt == nil {
			return m, nil
		}
		target := r.receipt.Identity
		if kind == "start" && (r.state != nil && (r.state.Active || r.state.OpenedDigest != "") || r.detail == nil || r.detail.Identity != target) {
			return m, nil
		}
		if kind == "resume" && (r.state == nil || r.state.Active || r.state.OpenedDigest == "" || (r.state.Gate != nil && r.state.Gate.Cleared) || (r.state.Watch != nil && r.state.Watch.PendingQuestion != nil)) {
			return m, nil
		}
		execute = func(ctx context.Context) journeyResult {
			state, err := api.ExecutionState(ctx, r.mission.ID, r.mission.WorkspaceID, r.receipt.ID)
			if err != nil {
				return journeyResult{err: err, effect: "rejected"}
			}
			if !journeyStateMatches(state, *r.receipt, r.state) || state.Active || (kind == "start" && state.OpenedDigest != "") || (kind == "resume" && (state.OpenedDigest == "" || (state.Gate != nil && state.Gate.Cleared) || (state.Watch != nil && state.Watch.PendingQuestion != nil))) {
				return journeyResult{err: domain.ErrInvalidReference, effect: "rejected"}
			}
			var result domain.ExecutionResult
			if kind == "start" {
				latest, readErr := api.PlanDetail(ctx, r.mission.ID, r.mission.WorkspaceID)
				if readErr != nil {
					return journeyResult{err: readErr, effect: "rejected"}
				}
				if latest.Identity != target || latest.Approval == nil || latest.Approval.ID != r.receipt.ID {
					return journeyResult{err: domain.ErrPlanStale, effect: "rejected"}
				}
				result, err = api.StartExecution(ctx, domain.ExecutionRequest{Target: target, ApprovalID: r.receipt.ID})
			} else {
				result, err = api.ResumeExecution(ctx, domain.ResumeRequest{Target: target, ApprovalID: r.receipt.ID, Delivery: r.state.Delivery, OpenedDigest: r.state.OpenedDigest})
			}
			return journeyResult{execution: result, err: err, effect: string(result.Effect)}
		}
	case "answer":
		if !r.fresh || r.question == nil || r.receipt == nil || r.state == nil || r.answer == "" || (r.answered != nil && *r.answered == r.question.Target) {
			return m, nil
		}
		question := r.question.Target
		for _, previous := range r.outcomes {
			if previous.answer.Target == question && (previous.answer.Effect == domain.AnswerUncertain || previous.answer.Effect == domain.AnswerRecorded) {
				return m, nil
			}
		}
		execute = func(ctx context.Context) journeyResult {
			state, err := api.ExecutionState(ctx, r.mission.ID, r.mission.WorkspaceID, r.receipt.ID)
			if err != nil {
				return journeyResult{err: err, effect: "rejected"}
			}
			if !journeyStateMatches(state, *r.receipt, r.state) || state.Watch == nil || state.Watch.PendingQuestion == nil || state.Watch.PendingQuestion.Target != question {
				return journeyResult{err: domain.ErrInvalidReference, effect: "rejected"}
			}
			out, err := api.AnswerQuestion(ctx, question.Answer(r.answer))
			return journeyResult{answer: out, err: err, effect: string(out.Effect)}
		}
	default:
		return m, nil
	}
	r.confirm = ""
	r.problem = ""
	m.putJourney(r)
	cmd := m.journeyRequest(kind, execute)
	return m, cmd
}
func (m model) applyJourney(out journeyResult) (model, tea.Cmd) {
	req := out.request
	if req == nil || m.journey.pending != req {
		return m, nil
	}
	m.journey.pending = nil
	r, ok := m.journey.records[req.key]
	if !ok {
		r = journeyRecord{mission: req.mission}
	}
	current := !m.exiting && req.epoch == m.journey.epoch && req.key == journeyID(m.roomMission()) && m.journeyVisible()
	read := req.kind == "refresh" || req.kind == "inspect" || req.kind == "source" || req.kind == "receipt" || req.kind == "sessions"
	if read && !current {
		return m, nil
	}
	r.effect = out.effect
	if !read {
		r.outcomes = append(append([]journeyResult{}, r.outcomes...), out)
	}
	if out.err != nil {
		r.fresh = false
		r.sourceFresh = false
		r.answerInspected = false
		r.problem = "journey_failed"
		if out.effect == "uncertain" {
			r.uncertain = req.kind
			r.problem = "journey_uncertain"
		}
		m.saveJourney(req.key, r)
		return m, nil
	}
	r.problem = ""
	if !read {
		r.fresh, r.sourceFresh = false, false
	}
	if out.detail != nil {
		changed := r.detail == nil || r.detail.Identity != out.detail.Identity
		if req.kind == "source" || r.detail == nil {
			if changed {
				r.page, r.inspected = 0, 0
			}
			r.detail = out.detail
		}
		if changed {
			r.sourceFresh = false
		}
		if req.kind == "source" {
			r.sourceFresh = current
		}
		if out.detail.Approval != nil {
			r.receipt = out.detail.Approval
		}
	}
	if out.state != nil {
		r.state = out.state
		r.nativeSnapshot = out.state.Bound && out.state.OpenedDigest != ""
		r.fresh = current
		r.watch = domain.WatchView{}
		if out.state.Watch != nil {
			r.watch = *out.state.Watch
		}
	}
	switch req.kind {
	case "source":
		r.mode = "source"
		r.confirm = ""
		r.inspected = max(r.inspected, min(journeyPageSize, len(journeySourceLines(*r.detail))))
	case "receipt":
		r.historical = out.receipt
		r.mode = "receipt"
		r.page = 0
	case "refresh", "inspect":
		if req.kind == "inspect" && out.mission != nil && r.uncertain == "bind" {
			r.mission = cloneMission(*out.mission)
			r.uncertain = ""
			r.fresh, r.sourceFresh = false, false
		}
		if req.kind == "inspect" && r.uncertain == "answer" && out.state != nil && out.state.Watch != nil && out.state.Watch.PendingQuestion != nil && r.question != nil && out.state.Watch.PendingQuestion.Target != r.question.Target {
			r.uncertain = ""
			r.answerInspected = false
		}
		if req.kind == "inspect" && r.uncertain == "answer" && out.state != nil && out.state.Bound && out.state.Watch != nil && out.state.Watch.PendingQuestion == nil && r.question != nil && r.question.Target.Delivery == out.state.Delivery && r.question.Target.OpenedDigest == out.state.OpenedDigest {
			r.answerInspected = true
		}
		if out.state == nil && out.watch != nil {
			r.watch = *out.watch
			r.fresh = current
		}
	case "planning":
		r.session = out.session
		r.mode = ""
	case "sessions":
		r.sessionCandidates = out.sessions
		if out.session != nil {
			r.session = out.session
			r.mode = ""
			if r.uncertain == "planning" {
				r.uncertain = ""
			}
		} else {
			r.mode = "session_id"
			r.focus = 0
		}
	case "bind":
		r.mission = cloneMission(*out.mission)
		r.detail = nil
		r.fresh = false
		r.mode = ""
	case "approve":
		r.receipt = out.receipt
		r.mode = ""
		r.fresh = false
	case "start", "resume":
		if out.execution.Effect == domain.ExecutionStarted {
			r.state = &domain.ExecutionState{Target: out.execution.Target, ApprovalID: out.execution.ApprovalID, Delivery: out.execution.Delivery, OpenedDigest: out.execution.OpenedDigest, Active: true}
		}
		r.mode = ""
		r.fresh = false
	case "answer":
		q := out.answer.Target
		r.answered = &q
		r.mode = ""
		r.fresh = false
	}
	if current && r.uncertain != "" && len(r.outcomes) > 0 {
		last := r.outcomes[len(r.outcomes)-1]
		if r.uncertain == "approve" && req.kind == "source" && out.detail != nil && out.detail.Approval != nil && out.detail.Identity == last.approval.Target {
			r.uncertain = ""
		}
		if (r.uncertain == "start" || (r.uncertain == "resume" && out.state != nil && out.state.Active)) && req.kind == "inspect" && out.state != nil && out.state.Bound && out.state.OpenedDigest != "" && out.state.Target == last.execution.Target && out.state.ApprovalID == last.execution.ApprovalID {
			r.uncertain = ""
		}
	}
	m.saveJourney(req.key, r)
	if current && (req.kind == "bind" || (req.kind == "inspect" && out.mission != nil)) {
		m.catalog.selected = cloneMission(r.mission)
	}
	if current && (req.kind == "refresh" || req.kind == "inspect" || out.state != nil) {
		m.watch = r.watch
	}
	if current && (req.kind == "planning" || req.kind == "sessions") && out.session != nil {
		found := false
		next := append([]domain.Session{}, m.sessions...)
		for i := range next {
			if next[i].ID == out.session.ID {
				next[i] = *out.session
				found = true
			}
		}
		if !found {
			next = append(next, *out.session)
		}
		m.sessions = next
	}
	return m, nil
}

func (m model) journeyErrorText(r journeyRecord) string {
	if len(r.outcomes) == 0 {
		return ""
	}
	out := r.outcomes[len(r.outcomes)-1]
	if out.err != nil {
		return fmt.Sprintf("%s: %s", out.request.kind, out.err)
	}
	return ""
}
