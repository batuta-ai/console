package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/plugins/limits"
)

type Service struct {
	Store   *store.Store
	Session *session.Manager
	Quota   *limits.Plugin
	Home    string
}

func New(st *store.Store, sess *session.Manager, lim *limits.Plugin) *Service {
	if lim == nil {
		lim = limits.New(nil)
	}
	s := &Service{Store: st, Session: sess, Quota: lim}
	if sess != nil {
		sess.OnExit = func(live domain.Session) {
			_ = s.Store.PutSession(live)
			_, _ = s.Store.AppendEvent("session.exited", map[string]any{"id": live.ID})
		}
	}
	return s
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
	}
	w := domain.Workspace{ID: domain.NewID("ws_"), Name: name, Root: abs, CreatedAt: domain.Now()}
	if err := s.Store.PutWorkspace(w); err != nil {
		return w, err
	}
	_, _ = s.Store.AppendEvent("workspace.created", map[string]any{"id": w.ID, "name": w.Name})
	return w, nil
}

func (s *Service) EnsureBatutaCatalog() error {
	for _, q := range batuta.BuiltInSquads() {
		if _, err := s.Store.GetSquad(q.ID); err == nil {
			continue
		} else if !store.IsNotFound(err) {
			return err
		}
		q.Draft = !s.ValidateDraft(q).Ready
		if err := s.Store.PutSquad(q); err != nil {
			return err
		}
		_, _ = s.Store.AppendEvent("squad.seeded", map[string]any{"id": q.ID, "origin": q.Origin})
	}
	return s.BindVacantMembers()
}

func (s *Service) SaveSquad(q domain.Squad) (domain.Squad, error) {
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
			return q, err
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
	q.Draft = !s.ValidateDraft(q).Ready
	if err := s.Store.PutSquad(q); err != nil {
		return q, err
	}
	_, _ = s.Store.AppendEvent("squad.saved", map[string]any{"id": q.ID, "version": q.Version})
	return q, nil
}

func (s *Service) ValidateSquad(id string) (domain.Validation, domain.Squad, error) {
	q, err := s.Store.GetSquad(id)
	if err != nil {
		return domain.Validation{}, q, err
	}
	return s.ValidateDraft(q), q, nil
}

func (s *Service) ValidateDraft(q domain.Squad) domain.Validation {
	v := domain.ValidateSquad(q)
	if !v.Ready {
		return v
	}
	pendingIsolation := false
	for _, m := range q.Members {
		if m.CLI == "" {
			continue
		}
		if ad, err := adapters.Get(m.CLI); err == nil {
			if _, err := ad.Lookup(); err != nil {
				v.Text += " CLI " + ad.CLI + " not on PATH."
			}
		}
		if m.AccountID == "" {
			continue
		}
		a, err := s.Store.GetAccount(m.AccountID)
		if err != nil {
			return domain.Validation{Tone: "warning", Title: "Validation: unknown account", Text: "A member points at an account that is not in the catalog. No model was invoked.", Ready: false}
		}
		if a.Isolation == domain.IsolationNotYetQualified {
			pendingIsolation = true
		}
	}
	if pendingIsolation {
		v.Tone = "warning"
		v.Title = "Validation: isolation pending"
		v.Text = "An account is not yet qualified for isolation. Validate probes capabilities; no paid task started."
	}
	return v
}

func (s *Service) CreateMission(workspaceID, title, squadID string) (domain.Mission, error) {
	if title == "" || workspaceID == "" {
		return domain.Mission{}, fmt.Errorf("workspace and title are required")
	}
	sq, err := s.Store.GetSquad(squadID)
	if err != nil {
		return domain.Mission{}, err
	}
	m := domain.Mission{
		ID: domain.NewID("msn_"), WorkspaceID: workspaceID, Title: title, SquadID: squadID,
		SquadSnapshot: sq, Status: "open", CreatedAt: domain.Now(),
	}
	if err := s.Store.PutMission(m); err != nil {
		return m, err
	}
	_, _ = s.Store.AppendEvent("mission.created", map[string]any{"id": m.ID, "title": m.Title, "squad": sq.ID, "squad_version": sq.Version})
	return m, nil
}

func (s *Service) CreateAccount(label, adapter string) (domain.Account, error) {
	a, err := adapters.NewAccount(label, adapter)
	if err != nil {
		return a, err
	}
	if err := s.Store.PutAccount(a); err != nil {
		return a, err
	}
	_, _ = s.Store.AppendEvent("account.created", map[string]any{"id": a.ID, "adapter": a.Adapter, "isolation": a.Isolation})
	return a, nil
}

func (s *Service) ConnectAccount(id string) (domain.Account, error) {
	a, err := s.Store.GetAccount(id)
	if err != nil {
		return a, err
	}
	ad, err := adapters.Get(a.Adapter)
	if err != nil {
		return a, err
	}
	dir := ad.IsolateDir(s.Home, a.ID)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return a, err
		}
		a.VaultRef = dir
	} else {
		a.VaultRef = "local:" + a.ID
	}
	a.Connected = true
	if err := s.Store.PutAccount(a); err != nil {
		return a, err
	}
	_, _ = s.Store.AppendEvent("account.connected", map[string]any{"id": a.ID, "adapter": a.Adapter, "isolate": a.VaultRef})
	if err := s.BindVacantMembers(); err != nil {
		return a, err
	}
	return a, nil
}

// BindVacantMembers assigns a connected account to squad members that have a
// CLI but no account, when exactly one connected account exists for that CLI.
func (s *Service) BindVacantMembers() error {
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
		if _, err := s.SaveSquad(next); err != nil {
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
	msn, err := s.Store.GetMission(in.MissionID)
	if err != nil {
		return domain.Session{}, err
	}
	ws, err := s.Store.GetWorkspace(msn.WorkspaceID)
	if err != nil {
		return domain.Session{}, err
	}
	var member domain.Member
	for _, m := range msn.SquadSnapshot.Members {
		if in.MemberID != "" && m.ID == in.MemberID {
			member = m
			break
		}
		if in.MemberID == "" && m.Maestro {
			member = m
		}
	}
	if in.MemberID == "" && member.ID == "" && len(msn.SquadSnapshot.Members) > 0 {
		member = msn.SquadSnapshot.Members[0]
	}
	var squadMember domain.Member
	if sq, err := s.Store.GetSquad(msn.SquadID); err == nil {
		for _, m := range sq.Members {
			if member.ID != "" && m.ID == member.ID {
				squadMember = m
				break
			}
			if member.Maestro && m.Maestro {
				squadMember = m
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
		return domain.Session{}, err
	}
	if in.AccountID != "" {
		acct, err := s.Store.GetAccount(in.AccountID)
		if err != nil {
			return domain.Session{}, fmt.Errorf("unknown account")
		}
		n, err := s.Store.CountRunningSessions(acct.ID)
		if err != nil {
			return domain.Session{}, err
		}
		if acct.SessionCap > 0 && n >= acct.SessionCap {
			return domain.Session{}, fmt.Errorf("account at session capacity %d", acct.SessionCap)
		}
	}
	argv := in.Argv
	if len(argv) == 0 {
		if path, err := ad.Lookup(); err == nil {
			argv = []string{path}
		} else {
			argv = []string{"/bin/sh"}
		}
	}
	profile := batuta.Prepare(ws.Root, in.inject())
	env := append([]string{}, profile.Env...)
	if len(bound.Skills) > 0 {
		env = append(env, "BATUTA_ROLE_SKILLS="+strings.Join(bound.Skills, ","))
	}
	if in.AccountID != "" {
		if acct, err := s.Store.GetAccount(in.AccountID); err == nil {
			acctAd := ad
			if a, err := adapters.Get(acct.Adapter); err == nil {
				acctAd = a
			}
			env = append(env, acctAd.IsolateEnv(s.Home, acct)...)
		}
	}
	sess := domain.Session{
		ID: domain.NewID("s_"), MissionID: msn.ID, WorkspaceID: ws.ID, MemberID: in.MemberID,
		AccountID: in.AccountID, Adapter: adapterID, Argv: argv, Process: "running", Work: "interactive",
		Owner: in.Owner, OwnerClient: in.Client, Cols: 110, Rows: 40, BatutaInjected: profile.Enabled,
		SkillsVersion: profile.SkillsVersion, CreatedAt: domain.Now(),
	}
	if sess.Owner == "" {
		sess.Owner = "none"
	}
	live, err := s.Session.Start(sess, env, ws.Root)
	if err != nil {
		return sess, err
	}
	if in.Client != "" && in.Owner != "" {
		live, _ = s.Session.Take(live.ID, in.Owner, in.Client)
	}
	if err := s.Store.PutSession(live); err != nil {
		return live, err
	}
	_, _ = s.Store.AppendEvent("session.started", map[string]any{"id": live.ID, "adapter": live.Adapter, "batuta": live.BatutaInjected})
	return live, nil
}

func (s *Service) ReconcileSession(sess domain.Session) domain.Session {
	if live, ok := s.Session.Snapshot(sess.ID); ok {
		return live
	}
	if sess.Process == "running" {
		sess.Process = "unknown"
	}
	return sess
}

func (s *Service) StopSession(id string) (domain.Session, error) {
	live, err := s.Session.Stop(id)
	if err != nil && err != os.ErrNotExist {
		return live, err
	}
	stored, err := s.Store.GetSession(id)
	if err != nil {
		return live, err
	}
	if live.ID != "" {
		stored = live
	} else {
		stored.Process = "exited"
	}
	_ = s.Store.PutSession(stored)
	_, _ = s.Store.AppendEvent("session.stopped", map[string]any{"id": id})
	return stored, nil
}

func (s *Service) Watch(workspaceID, delivery string) (domain.WatchView, error) {
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
	return batuta.Project(root, delivery)
}

func (s *Service) Answer(req batuta.AnswerRequest) (string, error) {
	root, err := s.authorizedRoot(req.Workspace)
	if err != nil {
		return "", err
	}
	req.Workspace = root
	id, err := batuta.Answer(req)
	if err != nil {
		return "", err
	}
	_, _ = s.Store.AppendEvent("watch.answered", map[string]any{"delivery": id, "task": req.Task})
	return id, nil
}

func (s *Service) authorizedRoot(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("workspace is required")
	}
	if ws, err := s.Store.GetWorkspace(ref); err == nil {
		return ws.Root, nil
	}
	abs, err := filepath.Abs(ref)
	if err != nil {
		return "", err
	}
	if ws, err := s.Store.GetWorkspaceByRoot(abs); err == nil {
		return ws.Root, nil
	}
	return "", fmt.Errorf("workspace not in catalog")
}

func (s *Service) Limits() ([]domain.LimitSnapshot, error) {
	accts, err := s.Store.ListAccounts()
	if err != nil {
		return nil, err
	}
	return s.Quota.All(accts), nil
}
