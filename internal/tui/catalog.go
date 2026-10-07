package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
)

const catalogTimeout = 15 * time.Second

type catalogFlow struct {
	epoch, revision, form                uint64
	focus                                int
	mission                              missionForm
	selected                             domain.Mission
	validation                           *domain.Validation
	pending, read, uncertain, inspection *catalogRequest
	inspected                            bool
	receipts                             []catalogResult
	problem, detail, notice              string
}
type catalogRequest struct {
	kind                                   string
	epoch, revision, form, accountRevision uint64
	squad                                  domain.Squad
	mission                                missionForm
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	lifetime                               *terminalLifetime
	once                                   sync.Once
	execute                                func(context.Context) catalogResult
	result                                 catalogResult
}
type catalogResult struct {
	request    *catalogRequest
	squad      domain.Squad
	mission    domain.Mission
	validation client.SquadValidation
	squads     []domain.Squad
	missions   []domain.Mission
	err        error
}
type catalogReadStart struct{}
type catalogJoined struct{ results []catalogResult }

func cloneSquad(q domain.Squad) domain.Squad {
	if q.Members != nil {
		q.Members = append([]domain.Member{}, q.Members...)
		for i := range q.Members {
			if q.Members[i].Skills != nil {
				q.Members[i].Skills = append([]string{}, q.Members[i].Skills...)
			}
		}
	}
	return q
}
func cloneMission(m domain.Mission) domain.Mission {
	m.SquadSnapshot = cloneSquad(m.SquadSnapshot)
	if m.Binding != nil {
		b := *m.Binding
		m.Binding = &b
	}
	return m
}
func (r *catalogRequest) run() tea.Msg {
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
func (m *model) newCatalogRequest(kind string) *catalogRequest {
	if m.catalogIO == nil {
		m.catalogIO = &terminalLifetime{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
	f := m.catalog.mission
	f.squad = cloneSquad(f.squad)
	return &catalogRequest{kind: kind, epoch: m.catalog.epoch, revision: m.catalog.revision, form: m.catalog.form, accountRevision: m.account.revision, squad: cloneSquad(m.editor), mission: f, ctx: ctx, cancel: cancel, lifetime: m.catalogIO}
}
func (m model) catalogBusy() bool { return m.catalog.pending != nil || m.catalog.uncertain != nil }
func (m model) catalogMutation(kind string) (model, tea.Cmd) {
	if m.api == nil || m.exiting || m.journeyBusy() || m.catalogBusy() || m.account.pending != nil || m.workspace.pending != nil || m.account.uncertain != nil || m.workspace.uncertain != nil {
		return m, nil
	}
	for _, out := range m.catalog.receipts {
		r := out.request
		if out.err != nil && r != nil && r.kind == kind && (r.form == m.catalog.form || (kind == "save" && r.squad.ID != "" && r.squad.ID == m.editor.ID)) {
			m.catalog.problem = "catalog_quarantined"
			return m, nil
		}
	}
	if kind == "save" && strings.TrimSpace(m.editor.Name) == "" {
		m.catalog.problem = "catalog_name_required"
		return m, nil
	}
	if kind == "create" && (!m.catalog.mission.reviewing || !m.missionValid()) {
		m.catalog.problem = "catalog_objective_required"
		return m, nil
	}
	m.catalog.revision++
	if m.catalog.read != nil {
		m.catalog.read.cancel()
	}
	r := m.newCatalogRequest(kind)
	api := m.api
	r.execute = func(ctx context.Context) catalogResult {
		out := catalogResult{}
		switch kind {
		case "validate":
			id := r.squad.ID
			if id == "" {
				id = "draft"
			}
			out.validation, out.err = api.ValidateSquad(ctx, id, &r.squad)
			if out.err == nil && (out.validation.Validation.Title == "" || !catalogValidationMatches(r.squad, out.validation.Squad)) {
				out.err = errors.New("invalid validation reply")
			}
		case "save":
			out.squad, out.err = api.SaveSquad(ctx, r.squad)
			if out.err == nil && !catalogSavedSquad(r.squad, out.squad) {
				out.err = catalogInvalidReply()
			}
		case "create":
			out.mission, out.err = api.CreateMission(ctx, r.mission.request())
			if out.err == nil && !catalogCreatedMission(r.mission, out.mission) {
				out.err = catalogInvalidReply()
			}
		}
		return out
	}
	m.catalog.pending = r
	m.catalog.problem, m.catalog.detail, m.catalog.notice = "", "", ""
	if kind == "validate" {
		m.catalog.validation = nil
	}
	return m, r.run
}
func catalogValidationMatches(in, out domain.Squad) bool {
	normalize := func(q domain.Squad) domain.Squad {
		q = cloneSquad(q)
		if q.ID == "" {
			q.ID = "draft"
		}
		if len(q.Members) == 0 {
			q.Members = nil
		}
		for i := range q.Members {
			if len(q.Members[i].Skills) == 0 {
				q.Members[i].Skills = nil
			}
		}
		return q
	}
	return reflect.DeepEqual(normalize(in), normalize(out))
}
func catalogInvalidReply() error {
	return &client.OperationError{Effect: client.EffectUncertain, Code: "catalog_invalid_reply"}
}
func catalogSavedSquad(in, out domain.Squad) bool {
	if !workspaceLine(out.ID, 8192) || (in.ID != "" && in.ID != out.ID) || out.Version < 1 {
		return false
	}
	ids := map[string]bool{}
	for _, mb := range out.Members {
		if !workspaceLine(mb.ID, 8192) || ids[mb.ID] {
			return false
		}
		ids[mb.ID] = true
	}
	for _, mb := range in.Members {
		if mb.ID != "" && !ids[mb.ID] {
			return false
		}
	}
	return len(in.Members) == len(out.Members)
}
func catalogCreatedMission(in missionForm, out domain.Mission) bool {
	return workspaceLine(out.ID, 8192) && out.WorkspaceID == in.workspaceID && out.SquadID == in.squad.ID && out.SquadSnapshot.ID == in.squad.ID && out.SquadSnapshot.Version > 0 && out.Status == "open" && out.Title == strings.TrimSpace(in.title) && out.Title != "" && out.Brief == strings.TrimSpace(in.brief) && out.Binding == nil
}
func (m *model) readCatalog() tea.Cmd {
	if m.api == nil || m.exiting || m.catalog.pending != nil {
		return nil
	}
	if m.catalog.read != nil {
		return m.catalog.read.run
	}
	r := m.newCatalogRequest("read")
	api := m.api
	r.execute = func(ctx context.Context) catalogResult {
		out := catalogResult{}
		out.squads, out.err = api.ListSquads(ctx)
		if out.err == nil {
			out.missions, out.err = api.ListMissions(ctx, "")
		}
		return out
	}
	m.catalog.read = r
	return r.run
}
func (m *model) inspectCatalog() tea.Cmd {
	cmd := m.readCatalog()
	m.catalog.inspection = m.catalog.read
	return cmd
}
func (m model) applyCatalogResult(out catalogResult) (model, tea.Cmd) {
	r := out.request
	if r == nil {
		return m, nil
	}
	if r == m.catalog.read {
		m.catalog.read = nil
		if m.exiting || r.epoch != m.catalog.epoch || r.revision != m.catalog.revision {
			return m, nil
		}
		if out.err != nil {
			m.catalog.problem = "catalog_read_failed"
			m.catalog.detail = out.err.Error()
			return m, nil
		}
		m.squads = make([]domain.Squad, len(out.squads))
		for i, q := range out.squads {
			m.squads[i] = cloneSquad(q)
		}
		m.missions = make([]domain.Mission, len(out.missions))
		for i, q := range out.missions {
			m.missions[i] = cloneMission(q)
		}
		if r == m.catalog.inspection && m.catalog.uncertain != nil {
			m.catalog.inspected = true
		}
		if m.catalog.uncertain == nil {
			m.catalog.problem, m.catalog.detail = "", ""
		}
		return m, nil
	}
	if r != m.catalog.pending {
		return m, nil
	}
	m.catalog.pending = nil
	current := !m.exiting && r.epoch == m.catalog.epoch && r.revision == m.catalog.revision && r.form == m.catalog.form
	m.catalog.revision++
	if r.kind == "validate" {
		if current && r.accountRevision == m.account.revision {
			if out.err != nil {
				m.catalog.problem = "catalog_validation_failed"
				m.catalog.detail = out.err.Error()
			} else {
				v := out.validation.Validation
				m.catalog.validation = &v
			}
		}
		return m, nil
	}
	if m.catalog.uncertain == r {
		m.catalog.uncertain = nil
	}
	if out.err != nil {
		var operation *client.OperationError
		if !errors.As(out.err, &operation) || (operation.Effect != client.EffectNotDispatched && operation.Effect != client.EffectRejected) {
			m.catalog.uncertain = r
			m.catalog.inspected = false
			m.catalog.problem = "catalog_uncertain"
			m.catalog.receipts = append(m.catalog.receipts, out)
		} else {
			m.catalog.problem = "catalog_retry"
		}
		m.catalog.detail = out.err.Error()
		return m, nil
	}
	m.catalog.receipts = append(m.catalog.receipts, out)
	m.catalog.problem, m.catalog.detail = "", ""
	m.catalog.notice = "catalog_completed_elsewhere"
	if r.kind == "save" {
		q := cloneSquad(out.squad)
		found := false
		for i := range m.squads {
			if m.squads[i].ID == q.ID {
				m.squads[i] = cloneSquad(q)
				found = true
				break
			}
		}
		if !found {
			m.squads = append(m.squads, cloneSquad(q))
		}
		if current {
			m.editor = q
			m.catalog.validation = nil
			m.catalog.notice = "catalog_saved"
		}
	} else {
		m.missions = append(m.missions, cloneMission(out.mission))
		if current {
			m = m.selectMission(out.mission)
			m.catalog.notice = "catalog_created"
			if !reflect.DeepEqual(r.mission.squad, out.mission.SquadSnapshot) {
				m.catalog.notice = "catalog_snapshot_changed"
			}
			return m, m.cancelWorkspaceRequests()
		}
	}
	return m, nil
}
func (m *model) cancelCatalogRequests() tea.Cmd {
	var requests []*catalogRequest
	for _, r := range []*catalogRequest{m.catalog.pending, m.catalog.read} {
		if r != nil {
			r.cancel()
			requests = append(requests, r)
		}
	}
	if len(requests) == 0 {
		return nil
	}
	return func() tea.Msg {
		out := catalogJoined{}
		for _, r := range requests {
			out.results = append(out.results, r.run().(catalogResult))
		}
		return out
	}
}
func (m model) acknowledgeCatalog() model {
	if m.catalog.uncertain != nil && m.catalog.inspected && m.catalog.pending == nil {
		m.catalog.uncertain = nil
		m.catalog.problem = "catalog_quarantined"
		m.catalog.detail = ""
	}
	return m
}
func (m model) viewCatalogNotice(b *strings.Builder) {
	if m.termMode {
		return
	}
	if m.catalog.pending != nil {
		fmt.Fprintln(b, i18n.T("catalog_pending"))
	}
	for _, key := range []string{m.catalog.problem, m.catalog.notice} {
		if key != "" {
			fmt.Fprintln(b, i18n.T(key))
		}
	}
	if m.catalog.detail != "" {
		fmt.Fprintln(b, m.catalog.detail)
	}
	if m.catalog.uncertain != nil {
		fmt.Fprintln(b, i18n.T("catalog_recovery_keys"))
	}
	if m.screen == scrSquads || m.screen == scrMissions {
		for _, out := range m.catalog.receipts {
			r := out.request
			if r == nil {
				continue
			}
			if out.err != nil {
				fmt.Fprintf(b, "%s: %s\n", i18n.T("catalog_uncertain"), r.kind)
				if r.kind == "create" {
					f := r.mission
					fmt.Fprintf(b, "%s: %s\n%s: %s %s v%d\n%s: %s\n%s: %s\n", i18n.T("catalog_workspace"), f.workspaceID, i18n.T("catalog_squad"), f.squad.ID, f.squad.Name, f.squad.Version, i18n.T("catalog_objective"), f.title, i18n.T("catalog_brief"), f.brief)
					fmt.Fprintf(b, "%s: %s\n%s: %s\n%s: %s\n%s: %s v%d\n", i18n.T("catalog_unverified_reply"), out.mission.ID, i18n.T("catalog_workspace"), out.mission.WorkspaceID, i18n.T("catalog_squad"), out.mission.SquadID, i18n.T("catalog_snapshot"), out.mission.SquadSnapshot.ID, out.mission.SquadSnapshot.Version)
					if r.squad.ID != "" {
						fmt.Fprintln(b, i18n.T("catalog_retained_editor"))
						viewCatalogReceiptSquad(b, r.squad)
					}
				} else {
					viewCatalogReceiptSquad(b, r.squad)
					fmt.Fprintf(b, "%s: %s\n", i18n.T("catalog_unverified_reply"), out.squad.ID)
				}
			} else {
				fmt.Fprintf(b, "%s: %s %s v%d %s\n", i18n.T("catalog_confirmed"), r.kind, out.squad.ID, out.squad.Version, out.mission.ID)
			}
		}
	}
}

func viewCatalogReceiptSquad(b *strings.Builder, q domain.Squad) {
	fmt.Fprintf(b, "%s: %s v%d %s %s\n%s: %s\n%s: %t\n", i18n.T("catalog_squad"), q.ID, q.Version, q.Origin, q.Name, i18n.T("catalog_purpose"), q.Purpose, i18n.T("catalog_structural_draft"), q.Draft)
	for _, mb := range q.Members {
		fmt.Fprintf(b, "%s %s %s %s %s %s %v %t\n", mb.ID, mb.Role, mb.CLI, mb.AccountID, mb.Model, mb.Effort, mb.Skills, mb.Maestro)
	}
}
