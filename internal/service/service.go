package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/plugins/limits"
)

type Service struct {
	Store                    *store.Store
	Session                  *session.Manager
	Quota                    *limits.Plugin
	Home                     string
	Readiness                *adapters.ReadinessChecker
	CoreExecutable           string
	NativeExecutorExecutable string
	native                   *nativeExecutions
	reuseMu                  sync.Mutex
	recoveryMu               sync.Mutex
}

func New(st *store.Store, sess *session.Manager, lim *limits.Plugin) *Service {
	if lim == nil {
		lim = limits.New(nil)
	}
	s := &Service{Store: st, Session: sess, Quota: lim, native: newNativeExecutions()}
	s.Readiness = adapters.NewReadinessChecker(s.diagnosticProbe(), nil)
	if sess != nil {
		sess.OnExit = func(live domain.Session) error {
			if err := s.Store.PutSessionWithEvent(live, "session.exited", map[string]any{"id": live.ID}); err != nil {
				log.Printf("persist session %s exit: %v", live.ID, err)
				return err
			}
			return nil
		}
	}
	return s
}

func (s *Service) DecorateWorkspace(w domain.Workspace) domain.Workspace {
	decorated, err := s.DecorateWorkspaceChecked(w)
	if err != nil {
		w.Profile = false
		w.Setup = domain.WorkspaceSetup{State: "partial", Code: "catalog_failure"}
		return w
	}
	return decorated
}

func (s *Service) DecorateAccount(a domain.Account) domain.Account {
	return s.DecorateAccountContext(context.Background(), a)
}

func (s *Service) DecorateAccountContext(ctx context.Context, a domain.Account) domain.Account {
	r := s.readinessChecker().Check(ctx, s.Home, a)
	a.Authed = r.Authenticated
	a.Readiness = &r
	return a
}

func (s *Service) CreateWorkspace(name, root string) (domain.Workspace, error) {
	if name == "" || root == "" {
		return domain.Workspace{}, fmt.Errorf("name and root are required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return domain.Workspace{}, err
	}
	if existing, err := s.Store.GetWorkspaceByRoot(abs); err == nil {
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.Workspace{}, catalogReferenceError{err}
	}
	w := domain.Workspace{ID: domain.NewID("ws_"), Name: name, Root: abs, CreatedAt: domain.Now()}
	if err := s.Store.PutWorkspaceWithEvent(w, "workspace.created", map[string]any{"id": w.ID, "name": w.Name}); err != nil {
		return w, catalogReferenceError{err}
	}
	return w, nil
}

func (s *Service) EnsureBatutaCatalog() error {
	for _, q := range batuta.BuiltInSquads() {
		if _, err := s.Store.GetSquad(q.ID); err == nil {
			continue
		} else if !store.IsNotFound(err) {
			return err
		}
		v, err := s.validateStructure(q)
		if err != nil {
			return err
		}
		q.Draft = !v.Ready
		if err := s.Store.PutSquadWithEvent(q, "squad.seeded", map[string]any{"id": q.ID, "origin": q.Origin}); err != nil {
			return err
		}
	}
	return s.BindVacantMembers()
}

func (s *Service) SaveSquad(q domain.Squad) (domain.Squad, error) {
	return s.SaveSquadContext(context.Background(), q)
}

func (s *Service) SaveSquadContext(ctx context.Context, q domain.Squad) (domain.Squad, error) {
	if q.ID == "" {
		q.ID = domain.NewID("sq_")
		q.Version = 1
		if q.Origin == "" {
			q.Origin = domain.OriginCustom
		}
	} else {
		prev, err := s.Store.GetSquad(q.ID)
		if err == nil {
			q.Version = prev.Version + 1
			if q.Origin == "" {
				q.Origin = prev.Origin
			}
		} else if !store.IsNotFound(err) {
			return q, catalogReferenceError{err}
		} else if q.Version == 0 {
			q.Version = 1
			if q.Origin == "" {
				q.Origin = domain.OriginCustom
			}
		}
	}
	for i := range q.Members {
		if q.Members[i].ID == "" {
			q.Members[i].ID = domain.NewID("mb_")
		}
	}
	v, err := s.validateStructure(q)
	if err != nil {
		return q, catalogReferenceError{err}
	}
	q.Draft = !v.Ready
	if err := s.Store.PutSquadWithEvent(q, "squad.saved", map[string]any{"id": q.ID, "version": q.Version}); err != nil {
		return q, catalogReferenceError{err}
	}
	return q, nil
}

func (s *Service) ValidateSquad(id string) (domain.Validation, domain.Squad, error) {
	return s.ValidateSquadContext(context.Background(), id)
}

func (s *Service) ValidateSquadContext(ctx context.Context, id string) (domain.Validation, domain.Squad, error) {
	q, err := s.Store.GetSquad(id)
	if err != nil {
		return domain.Validation{}, q, catalogReferenceError{err}
	}
	return s.ValidateDraftContext(ctx, q), q, nil
}

func (s *Service) ValidateDraft(q domain.Squad) domain.Validation {
	return s.ValidateDraftContext(context.Background(), q)
}

func (s *Service) ValidateDraftContext(ctx context.Context, q domain.Squad) domain.Validation {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := s.validateStructure(q)
	if err != nil {
		return catalogValidation(err)
	}
	if !v.Ready {
		return v
	}
	checked := make(map[string]domain.AccountReadiness)
	for _, m := range q.Members {
		a, err := s.Store.GetAccount(m.AccountID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return catalogValidation(err)
			}
			return domain.Validation{Tone: "warning", Title: "Validation: unknown account", Text: "A member points at an account that is not in the catalog. No model was invoked.", Ready: false}
		}
		if a.Adapter != m.CLI {
			return domain.Validation{Tone: "warning", Title: "Validation: account adapter mismatch", Text: "Select an account for this member's CLI.", Ready: false}
		}
		if a.Isolation == domain.IsolationNotYetQualified {
			return domain.Validation{Tone: "warning", Title: "Validation: isolation pending", Text: "This account's isolation is not yet qualified.", Ready: false}
		}
		r, ok := checked[a.ID]
		if !ok {
			r = s.readinessChecker().Check(ctx, s.Home, a)
			checked[a.ID] = r
		}
		if !r.Ready {
			return domain.Validation{Tone: "warning", Title: "Validation: " + r.Code, Text: "Check this account's readiness and repair its connection, CLI or login before execution.", Ready: false}
		}
	}
	return v
}

func (s *Service) CreateMission(workspaceID, title, brief, squadID string) (domain.Mission, error) {
	title = strings.TrimSpace(title)
	brief = strings.TrimSpace(brief)
	if title == "" || workspaceID == "" {
		return domain.Mission{}, invalidReference("workspace and title are required")
	}
	if _, err := s.Store.GetWorkspace(workspaceID); err != nil {
		return domain.Mission{}, catalogReferenceError{fmt.Errorf("workspace %q: %w", workspaceID, err)}
	}
	sq, err := s.Store.GetSquad(squadID)
	if err != nil {
		return domain.Mission{}, catalogReferenceError{err}
	}
	m := domain.Mission{
		ID: domain.NewID("msn_"), WorkspaceID: workspaceID, Title: title, Brief: brief, SquadID: squadID,
		SquadSnapshot: sq, Status: "open", CreatedAt: domain.Now(),
	}
	if err := s.Store.PutMissionWithEvent(m, "mission.created", map[string]any{"id": m.ID, "title": m.Title, "squad": sq.ID, "squad_version": sq.Version}); err != nil {
		return m, catalogReferenceError{err}
	}
	return m, nil
}

func (s *Service) CreateAccount(label, adapter string) (domain.Account, error) {
	a, err := adapters.NewAccount(label, adapter)
	if err != nil {
		return a, err
	}
	if err := s.Store.PutAccountWithEvent(a, "account.created", map[string]any{"id": a.ID, "adapter": a.Adapter, "isolation": a.Isolation}); err != nil {
		return a, err
	}
	return a, nil
}

func (s *Service) RenameAccount(id, label string) (domain.Account, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return domain.Account{}, fmt.Errorf("account label is required")
	}
	a, err := s.Store.GetAccount(id)
	if err != nil {
		return a, err
	}
	if a.Label == label {
		return a, nil
	}
	a.Label = label
	if err := s.Store.PutAccountWithEvent(a, "account.renamed", map[string]any{"id": a.ID, "label": a.Label}); err != nil {
		return a, err
	}
	return a, nil
}

func (s *Service) ConnectAccount(id string) (domain.Account, error) {
	return s.ConnectAccountContext(context.Background(), id)
}

func (s *Service) ConnectAccountContext(ctx context.Context, id string) (domain.Account, error) {
	a, err := s.Store.GetAccount(id)
	if err != nil {
		return a, err
	}
	ad, err := adapters.Get(a.Adapter)
	if err != nil {
		return a, err
	}
	if _, err := ad.IsolateEnv(s.Home, a); err != nil {
		return a, err
	}
	a.VaultRef = ad.IsolateDir(s.Home, a.ID)
	a.Connected = true
	if err := s.Store.PutAccountWithEvent(a, "account.connected", map[string]any{"id": a.ID, "adapter": a.Adapter, "isolate": a.VaultRef}); err != nil {
		return a, err
	}
	if err := s.BindVacantMembersContext(ctx); err != nil {
		return a, err
	}
	return a, nil
}

// BindVacantMembers assigns a connected account to squad members that have a
// CLI but no account, when exactly one connected account exists for that CLI.
func (s *Service) BindVacantMembers() error {
	return s.BindVacantMembersContext(context.Background())
}

func (s *Service) BindVacantMembersContext(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	accounts, err := s.Store.ListAccounts()
	if err != nil {
		return err
	}
	byCLI := uniqueConnected(accounts)
	if len(byCLI) == 0 {
		return nil
	}
	squads, err := s.Store.ListSquads()
	if err != nil {
		return err
	}
	for _, q := range squads {
		next, changed := bindVacant(q, byCLI)
		if !changed {
			continue
		}
		if _, err := s.SaveSquadContext(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

func uniqueConnected(accounts []domain.Account) map[string]string {
	ids := map[string][]string{}
	for _, a := range accounts {
		if !a.Connected || a.Adapter == "" {
			continue
		}
		ids[a.Adapter] = append(ids[a.Adapter], a.ID)
	}
	out := map[string]string{}
	for cli, list := range ids {
		if len(list) == 1 {
			out[cli] = list[0]
		}
	}
	return out
}

func bindVacant(q domain.Squad, byCLI map[string]string) (domain.Squad, bool) {
	changed := false
	for i, m := range q.Members {
		if m.AccountID != "" || m.CLI == "" {
			continue
		}
		id, ok := byCLI[m.CLI]
		if !ok {
			continue
		}
		q.Members[i].AccountID = id
		changed = true
	}
	return q, changed
}

// StartSessionIn always references a catalog mission and an existing workspace
// directory. Inject=false skips Batuta injection, not reference validation.
// Empty snapshots are permitted only with explicit injection opt-out and no member.
type StartSessionIn struct {
	MissionID string   `json:"mission_id"`
	MemberID  string   `json:"member_id,omitempty"`
	AccountID string   `json:"account_id,omitempty"`
	Adapter   string   `json:"adapter"`
	Argv      []string `json:"argv,omitempty"`
	Inject    *bool    `json:"batuta"`
	Owner     string   `json:"owner,omitempty"`
	Client    string   `json:"client,omitempty"`
}

func (in StartSessionIn) inject() bool {
	if in.Inject != nil {
		return *in.Inject
	}
	return true
}

func (s *Service) StartSession(in StartSessionIn) (domain.Session, error) {
	return s.StartSessionContext(context.Background(), in)
}

func (s *Service) StartSessionContext(ctx context.Context, in StartSessionIn) (domain.Session, error) {
	msn, err := s.Store.GetMission(in.MissionID)
	if err != nil {
		return domain.Session{}, catalogReferenceError{err}
	}
	ws, err := s.Store.GetWorkspace(msn.WorkspaceID)
	if err != nil {
		return domain.Session{}, catalogReferenceError{err}
	}
	info, err := os.Stat(ws.Root)
	if err != nil {
		return domain.Session{}, fmt.Errorf("%w: workspace root: %w", domain.ErrInvalidReference, err)
	}
	if !info.IsDir() {
		return domain.Session{}, invalidReference("workspace root is not a directory")
	}
	member, err := domain.SelectSnapshotMember(msn.SquadSnapshot, in.MemberID, in.inject())
	if err != nil {
		return domain.Session{}, err
	}
	var squadMember domain.Member
	sq, err := s.Store.GetSquad(msn.SquadID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, catalogReferenceError{fmt.Errorf("current squad: %w", err)}
	}
	if err == nil && member.ID != "" {
		for _, m := range sq.Members {
			if m.ID == member.ID {
				squadMember = m
				break
			}
		}
	}
	bound := domain.ResolveBinding(domain.Member{}, domain.Member{}, member, squadMember)
	if in.AccountID == "" && bound.AccountID != "" {
		in.AccountID = bound.AccountID
	}
	adapterID := in.Adapter
	if adapterID == "" && bound.CLI != "" {
		adapterID = bound.CLI
	}
	if adapterID == "" {
		adapterID = "codex"
	}
	ad, err := adapters.Get(adapterID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("%w: %w", domain.ErrInvalidReference, err)
	}
	capacity := 0
	var selectedAccount domain.Account
	var accountEnv []string
	if in.AccountID != "" {
		acct, err := s.Store.GetAccount(in.AccountID)
		if err != nil {
			return domain.Session{}, catalogReferenceError{fmt.Errorf("account %q: %w", in.AccountID, err)}
		}
		if acct.Adapter != ad.ID {
			return domain.Session{}, invalidReference("account adapter does not match selected adapter")
		}
		capacity = acct.SessionCap
		selectedAccount = acct
	}

	command, err := ad.Command(adapters.CommandRequest{
		Mode: "interactive", Prompt: sessionPrompt(in.inject(), msn.Brief),
		Model: bound.Model, Effort: bound.Effort, Argv: in.Argv,
	})
	if err != nil {
		return domain.Session{}, err
	}
	if command.Report.Mode != "custom" {
		if selectedAccount.ID == "" {
			return domain.Session{}, ReadinessError{Code: "account_required"}
		}
		r := s.readinessChecker().Check(ctx, s.Home, selectedAccount)
		if !r.Ready {
			return domain.Session{}, ReadinessError{Code: r.Code, AccountID: selectedAccount.ID}
		}
	}
	if selectedAccount.ID != "" {
		accountEnv, err = ad.IsolateEnv(s.Home, selectedAccount)
		if err != nil {
			return domain.Session{}, err
		}
	}

	requiredSkills := append([]string{}, bound.Skills...)
	if in.inject() && command.Report.Mode != "custom" && strings.TrimSpace(msn.Brief) != "" {
		requiredSkills = append(requiredSkills, "batuta-plan")
	}
	profile, err := prepareSessionSkills(ws.Root, in.inject(), ad.ID, accountEnv, requiredSkills)
	if err != nil {
		return domain.Session{}, err
	}
	env := append([]string{}, profile.Env...)
	if len(bound.Skills) > 0 {
		env = append(env, "BATUTA_ROLE_SKILLS="+strings.Join(bound.Skills, ","))
	}
	if msn.Brief != "" {
		env = append(env, "BATUTA_MISSION="+strings.Join(strings.Fields(msn.Brief), " "))
	}
	env = append(env, accountEnv...)
	sess := domain.Session{
		ID: domain.NewID("s_"), MissionID: msn.ID, WorkspaceID: ws.ID, MemberID: member.ID,
		AccountID: in.AccountID, Adapter: adapterID, Argv: command.Argv, Process: "running", Work: "interactive",
		Owner: in.Owner, OwnerClient: in.Client, Cols: 110, Rows: 40, BatutaInjected: profile.Enabled,
		SkillsVersion: profile.SkillsVersion, CreatedAt: domain.Now(),
	}
	if sess.Owner == "" {
		sess.Owner = "none"
	}
	return s.startAndPersist(sess, env, ws.Root, capacity, "session.started", map[string]any{"id": sess.ID, "adapter": sess.Adapter, "batuta": sess.BatutaInjected, "command": command.Report})
}

// ReconcileSessions must finish before the daemon opens its listener.
func (s *Service) ReconcileSessions() error {
	list, err := s.Store.ListSessions()
	if err != nil {
		return fmt.Errorf("list sessions for recovery: %w", err)
	}
	for _, sess := range list {
		if _, err := s.ReconcileSession(sess); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ReconcileSession(sess domain.Session) (domain.Session, error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	// Capture ownership before the catalog read: exit persistence and removal
	// may otherwise turn a stale running row into a false lost-session event.
	live, owned := s.Session.Snapshot(sess.ID)
	// Reload under the lock: concurrent GETs may carry the same stale row.
	stored, err := s.Store.GetSession(sess.ID)
	if err != nil {
		return stored, err
	}
	if stored.Process == "exited" {
		return stored, nil
	}
	if owned {
		return live, nil
	}
	if stored.Process != "running" && (stored.Process != "unknown" || stored.RecoveryReason != "") {
		return stored, nil
	}
	observed, inspectionErr := session.InspectProcess(stored.PID)
	reason := "identity_unverified"
	if inspectionErr == nil {
		switch {
		case observed.Missing:
			reason = "process_missing"
		case stored.ProcessIdentity == "" || observed.Identity == "":
		case stored.ProcessIdentity == observed.Identity:
			reason = "process_survived"
		default:
			reason = "identity_mismatch"
		}
	}
	stored.Process = "unknown"
	stored.RecoveryReason = reason
	stored.Owner, stored.OwnerClient = "none", ""
	payload := map[string]any{"id": stored.ID, "recovery_reason": reason}
	if inspectionErr != nil {
		payload["inspection_error"] = inspectionErr.Error()
	}
	if err := s.Store.PutSessionWithEvent(stored, "session.reconciled", payload); err != nil {
		return stored, fmt.Errorf("reconcile session %s: %w", stored.ID, err)
	}
	return stored, nil
}

var ErrSessionNotOwned = errors.New("session process is not owned by this daemon; former transport cannot be reattached or controlled; start a new session")

func (s *Service) StopSession(id string) (domain.Session, error) {
	return s.StopSessionContext(context.Background(), id)
}

func (s *Service) StopSessionContext(ctx context.Context, id string) (domain.Session, error) {
	return s.Session.StopAndFinalize(ctx, id, s.finalizeStop)
}

func stopPersistenceError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return &session.StopPersistenceError{Cause: err}
}

func (s *Service) finalizeStop(id string, live domain.Session, err error) (domain.Session, error) {
	if err != nil && err != os.ErrNotExist {
		return live, err
	}
	stored, err := s.Store.GetSession(id)
	if err != nil {
		return live, stopPersistenceError(err)
	}
	if live.ID != "" {
		stored = live
	} else {
		if stored.Process == "exited" {
			return stored, nil
		}
		stored, err = s.ReconcileSession(stored)
		if err != nil {
			return stored, stopPersistenceError(err)
		}
		return stored, ErrSessionNotOwned
	}
	if err := s.Store.PutSessionWithEvent(stored, "session.stopped", map[string]any{"id": id}); err != nil {
		return stored, stopPersistenceError(err)
	}
	return stored, nil
}

func (s *Service) reuseLive(match func(domain.Session) bool, owner, client string) (domain.Session, bool, error) {
	live, ok, err := s.findLive(match)
	if err != nil || !ok {
		return live, ok, err
	}
	if owner != "" && client != "" {
		taken, err := s.Session.Take(live.ID, owner, client)
		if err != nil {
			return live, false, err
		}
		live = taken
		if err := s.Store.PutSession(live); err != nil {
			return live, false, catalogReferenceError{err}
		}
	}
	return live, true, nil
}

func (s *Service) findLive(match func(domain.Session) bool) (domain.Session, bool, error) {
	list, err := s.Store.ListSessions()
	if err != nil {
		return domain.Session{}, false, catalogReferenceError{err}
	}
	for _, sess := range list {
		if !match(sess) {
			continue
		}
		live, err := s.ReconcileSession(sess)
		if err != nil {
			return live, false, catalogReferenceError{err}
		}
		if live.Process != "running" {
			continue
		}
		return live, true, nil
	}
	return domain.Session{}, false, nil
}

func (s *Service) StartLogin(accountID, owner, client string) (domain.Session, error) {
	return s.StartLoginContext(context.Background(), accountID, owner, client)
}

func (s *Service) StartLoginContext(ctx context.Context, accountID, owner, client string) (domain.Session, error) {
	a, err := s.Store.GetAccount(accountID)
	if err != nil {
		return domain.Session{}, err
	}
	if !a.Connected {
		return domain.Session{}, fmt.Errorf("connect the account folder before login")
	}
	ad, err := adapters.Get(a.Adapter)
	if err != nil {
		return domain.Session{}, err
	}
	command, err := ad.Command(adapters.CommandRequest{Mode: "login"})
	if err != nil {
		return domain.Session{}, err
	}
	r := s.readinessChecker().CheckCLI(ctx, s.Home, a)
	if !r.CLICompatible {
		return domain.Session{}, ReadinessError{Code: r.Code, AccountID: a.ID}
	}
	s.reuseMu.Lock()
	defer s.reuseMu.Unlock()
	if ctx.Err() != nil {
		return domain.Session{}, ReadinessError{Code: "check_cancelled", AccountID: a.ID}
	}
	if live, ok, err := s.reuseLive(func(sess domain.Session) bool {
		return sess.AccountID == a.ID && sess.Work == "login"
	}, owner, client); err != nil {
		return live, err
	} else if ok {
		return live, nil
	}
	sess := domain.Session{
		ID: domain.NewID("s_"), AccountID: a.ID, Adapter: ad.ID, Argv: command.Argv,
		Process: "running", Work: "login", Owner: owner, OwnerClient: client,
		Cols: 110, Rows: 40, CreatedAt: domain.Now(),
	}
	if sess.Owner == "" {
		sess.Owner = "web"
	}
	env, err := ad.IsolateEnv(s.Home, a)
	if err != nil {
		return domain.Session{}, err
	}
	return s.startAndPersist(sess, env, ad.IsolateDir(s.Home, a.ID), a.SessionCap, "account.login", map[string]any{"id": a.ID, "session": sess.ID, "command": command.Report})
}

func (s *Service) StartInit(workspaceID, brief, owner, client string, input ...InitInput) (domain.Session, error) {
	return s.StartInitContext(context.Background(), workspaceID, brief, owner, client, input...)
}

func (s *Service) StartInitContext(ctx context.Context, workspaceID, brief, owner, client string, input ...InitInput) (domain.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, err := s.Store.GetWorkspace(workspaceID)
	if err != nil {
		return domain.Session{}, catalogReferenceError{err}
	}
	if err := batuta.ValidateSetupRoot(ws.Root); err != nil {
		return domain.Session{}, invalidReference("%s", err)
	}
	if len(input) == 0 || strings.TrimSpace(input[0].AccountID) == "" {
		return domain.Session{}, ReadinessError{Code: "authenticated_account_required"}
	}
	if len(input) != 1 {
		return domain.Session{}, invalidReference("exactly one confirmed setup input is required")
	}
	answers, err := confirmedInitAnswers(input[0].Config, brief)
	if err != nil {
		return domain.Session{}, err
	}
	if ctx.Err() != nil {
		return domain.Session{}, ReadinessError{Code: "check_cancelled", AccountID: input[0].AccountID}
	}
	ad, command, extra, selected, err := s.initCLI(ctx, initPrompt(answers), input[0].AccountID)
	if err != nil {
		return domain.Session{}, err
	}

	s.reuseMu.Lock()
	defer s.reuseMu.Unlock()
	if ctx.Err() != nil {
		return domain.Session{}, ReadinessError{Code: "check_cancelled", AccountID: selected.ID}
	}
	current, active, err := s.findLive(func(sess domain.Session) bool { return sess.WorkspaceID == ws.ID && sess.Work == "init" })
	if err != nil {
		return current, err
	}
	if active && !sameInit(current, selected.ID, command.Argv) {
		return domain.Session{}, ErrInitConflict
	}
	if live, ok, err := s.reuseLive(func(sess domain.Session) bool {
		return sess.WorkspaceID == ws.ID && sess.Work == "init" && sess.AccountID == selected.ID
	}, owner, client); err != nil {
		return live, err
	} else if ok {
		return live, nil
	}
	if batuta.ReadSetup(ws.Root).Ready {
		return domain.Session{}, fmt.Errorf("%w: workspace configuration is already complete", ErrInitConflict)
	}
	profile, err := prepareSessionSkills(ws.Root, true, ad.ID, extra, []string{"batuta-init"})
	if err != nil {
		return domain.Session{}, err
	}
	env := append(append([]string{}, profile.Env...), extra...)
	env = append(env, "BATUTA_INIT="+strings.Join(strings.Fields(brief), " "))
	sess := domain.Session{
		ID: domain.NewID("s_"), WorkspaceID: ws.ID, AccountID: selected.ID, Adapter: ad.ID, Argv: command.Argv,
		Process: "running", Work: "init", Owner: owner, OwnerClient: client,
		Cols: 110, Rows: 40, BatutaInjected: profile.Enabled, SkillsVersion: profile.SkillsVersion,
		CreatedAt: domain.Now(),
	}
	if sess.Owner == "" {
		sess.Owner = "web"
	}
	if ctx.Err() != nil {
		return domain.Session{}, ReadinessError{Code: "check_cancelled", AccountID: selected.ID}
	}
	if err := batuta.ValidateSetupRoot(ws.Root); err != nil {
		return domain.Session{}, invalidReference("%s", err)
	}
	return s.startAndPersist(sess, env, ws.Root, selected.SessionCap, "workspace.init", map[string]any{"id": ws.ID, "session": sess.ID, "account_id": selected.ID, "command": command.Report})
}

func sessionPrompt(inject bool, brief string) string {
	if strings.TrimSpace(brief) == "" {
		return ""
	}
	if !inject {
		return brief
	}
	brief = strings.TrimSpace(brief)
	return "Use batuta-plan for this mission. Do not implement yet. Write .batuta/plans/<slug>.md with tasks, scope and acceptance criteria, set Status to proposed, and wait for approval.\n\nGoal:\n" + brief
}

func initPrompt(brief string) string {
	return "Run batuta-init for this project. Write .batuta/profile.md, .batuta/routing.md and WORK.md from these confirmed answers, preserving existing user content, then stop.\n\n" + strings.TrimSpace(brief)
}

func (s *Service) initCLI(ctx context.Context, prompt, accountID string) (adapters.Adapter, adapters.Command, []string, domain.Account, error) {
	if accountID == "" {
		return adapters.Adapter{}, adapters.Command{}, nil, domain.Account{}, ReadinessError{Code: "authenticated_account_required"}
	}
	a, err := s.Store.GetAccount(accountID)
	if err != nil {
		return adapters.Adapter{}, adapters.Command{}, nil, a, catalogReferenceError{err}
	}
	ad, err := adapters.Get(a.Adapter)
	if err != nil || (a.Adapter != "codex" && a.Adapter != "claude") {
		return ad, adapters.Command{}, nil, a, ReadinessError{Code: "adapter_unsupported", AccountID: a.ID}
	}
	command, err := ad.Command(adapters.CommandRequest{Mode: "init", Prompt: prompt})
	if err != nil {
		return ad, command, nil, a, err
	}
	r := s.readinessChecker().Check(ctx, s.Home, a)
	if !r.Ready {
		return ad, command, nil, a, ReadinessError{Code: r.Code, AccountID: a.ID}
	}
	env, err := ad.IsolateEnv(s.Home, a)
	return ad, command, env, a, err
}

func (s *Service) Watch(workspaceID, delivery string) (domain.WatchView, error) {
	return s.WatchContext(context.Background(), workspaceID, delivery)
}

func (s *Service) WatchContext(ctx context.Context, workspaceID, delivery string) (domain.WatchView, error) {
	root := ""
	if workspaceID != "" {
		ws, err := s.Store.GetWorkspace(workspaceID)
		if err != nil && err != sql.ErrNoRows {
			return domain.WatchView{}, err
		}
		if err == nil {
			root = ws.Root
		}
	}
	if root == "" {
		return domain.WatchView{API: "v0", Message: "workspace root required"}, nil
	}
	view, err := batuta.ProjectContext(ctx, root, delivery)
	if err != nil {
		return view, err
	}
	view.Plans = batuta.ListPlans(root)
	view.Scope = "workspace"
	view.WorkspaceID = workspaceID
	return view, nil
}

// Answer retains the delivery/error contract for existing Go callers.
func (s *Service) Answer(req batuta.AnswerRequest) (string, error) {
	result, err := s.AnswerQuestion(context.Background(), req)
	return result.Delivery, err
}

func (s *Service) Limits() ([]domain.LimitSnapshot, error) {
	accts, err := s.Store.ListAccounts()
	if err != nil {
		return nil, err
	}
	return s.Quota.All(accts), nil
}

func (s *Service) startAndPersist(sess domain.Session, env []string, dir string, capacity int, kind string, payload map[string]any) (domain.Session, error) {
	pending, err := s.Session.StartPending(sess, env, dir, capacity)
	if err != nil {
		return sess, err
	}
	live, err := pending.Persist(func(latest domain.Session) error {
		if err := s.Store.PutSessionWithEvent(latest, kind, payload); err != nil {
			return catalogReferenceError{err}
		}
		return nil
	})
	if err != nil {
		return live, s.stopAfterFailure(live.ID, err)
	}
	return live, nil
}

func (s *Service) stopAfterFailure(id string, cause error) error {
	stopped, err := s.Session.Stop(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, fmt.Errorf("cleanup session %s: %w", id, err))
	}
	if err == nil && stopped.Process != "exited" {
		return errors.Join(cause, fmt.Errorf("cleanup session %s: process still %s", id, stopped.Process))
	}
	return cause
}

func invalidReference(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidReference, fmt.Sprintf(format, args...))
}

// CatalogReferenceFailure identifies catalog I/O without classifying process or
// account-admission errors as storage failures.
func CatalogReferenceFailure(err error) bool {
	var failure catalogReferenceError
	return errors.As(err, &failure)
}

type catalogReferenceError struct{ error }

func (e catalogReferenceError) Unwrap() error { return e.error }
