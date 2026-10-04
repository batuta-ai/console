package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// Share one connection so reads, bare writes and transactions cannot
	// contend for SQLite locks within this Store.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s catalogWriter) PutWorkspace(w domain.Workspace) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO workspaces(id,name,root,created_at) VALUES(?,?,?,?)`, w.ID, w.Name, w.Root, w.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListWorkspaces() ([]domain.Workspace, error) {
	rows, err := s.db.Query(`SELECT id,name,root,created_at FROM workspaces ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Workspace
	for rows.Next() {
		var w domain.Workspace
		var at string
		if err := rows.Scan(&w.ID, &w.Name, &w.Root, &at); err != nil {
			return nil, err
		}
		w.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, w)
	}
	if out == nil {
		out = []domain.Workspace{}
	}
	return out, rows.Err()
}

func (s *Store) GetWorkspace(id string) (domain.Workspace, error) {
	var w domain.Workspace
	var at string
	err := s.db.QueryRow(`SELECT id,name,root,created_at FROM workspaces WHERE id=?`, id).Scan(&w.ID, &w.Name, &w.Root, &at)
	if err != nil {
		return w, err
	}
	w.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return w, nil
}

func (s *Store) GetWorkspaceByRoot(root string) (domain.Workspace, error) {
	var w domain.Workspace
	var at string
	err := s.db.QueryRow(`SELECT id,name,root,created_at FROM workspaces WHERE root=?`, root).Scan(&w.ID, &w.Name, &w.Root, &at)
	if err != nil {
		return w, err
	}
	w.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return w, nil
}

func (s catalogWriter) PutSquad(q domain.Squad) error {
	b, err := json.Marshal(q.Members)
	if err != nil {
		return err
	}
	draft := 0
	if q.Draft {
		draft = 1
	}
	if q.Origin == "" {
		q.Origin = domain.OriginCustom
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO squads(id,name,purpose,version,draft,members,origin) VALUES(?,?,?,?,?,?,?)`, q.ID, q.Name, q.Purpose, q.Version, draft, string(b), q.Origin)
	return err
}

func decodeSquad(q domain.Squad, draft int, members string) domain.Squad {
	q.Draft = draft == 1
	_ = json.Unmarshal([]byte(members), &q.Members)
	if q.Members == nil {
		q.Members = []domain.Member{}
	}
	if q.Origin == "" {
		q.Origin = domain.OriginCustom
	}
	return q
}

func (s *Store) GetSquad(id string) (domain.Squad, error) {
	var q domain.Squad
	var members string
	var draft int
	err := s.db.QueryRow(`SELECT id,name,purpose,version,draft,members,origin FROM squads WHERE id=?`, id).Scan(&q.ID, &q.Name, &q.Purpose, &q.Version, &draft, &members, &q.Origin)
	if err != nil {
		return q, err
	}
	return decodeSquad(q, draft, members), nil
}

func (s *Store) ListSquads() ([]domain.Squad, error) {
	rows, err := s.db.Query(`SELECT id,name,purpose,version,draft,members,origin FROM squads ORDER BY origin DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Squad
	for rows.Next() {
		var q domain.Squad
		var members string
		var draft int
		if err := rows.Scan(&q.ID, &q.Name, &q.Purpose, &q.Version, &draft, &members, &q.Origin); err != nil {
			return nil, err
		}
		out = append(out, decodeSquad(q, draft, members))
	}
	if out == nil {
		out = []domain.Squad{}
	}
	return out, rows.Err()
}

// PutMission preserves binding columns; only BindMission may change ownership.
func (s catalogWriter) PutMission(m domain.Mission) error {
	b, err := json.Marshal(m.SquadSnapshot)
	if err != nil {
		return err
	}
	result, err := s.db.Exec(`INSERT INTO missions(id,workspace_id,title,squad_id,snapshot,status,attention,created_at,brief) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET workspace_id=excluded.workspace_id,title=excluded.title,squad_id=excluded.squad_id,snapshot=excluded.snapshot,status=excluded.status,attention=excluded.attention,created_at=excluded.created_at,brief=excluded.brief
WHERE missions.plan_path='' OR missions.workspace_id=excluded.workspace_id`,
		m.ID, m.WorkspaceID, m.Title, m.SquadID, string(b), m.Status, m.Attention, m.CreatedAt.Format(time.RFC3339Nano), m.Brief)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return domain.ErrBindingConflict
	}
	return err
}

func (s *Store) GetMission(id string) (domain.Mission, error) {
	var m domain.Mission
	var snap, at string
	var binding domain.MissionBinding
	err := s.db.QueryRow(`SELECT id,workspace_id,title,squad_id,snapshot,status,attention,created_at,brief,binding_root,binding_canonical_root,plan_path,plan_slug,plan_digest,delivery_id,opened_digest FROM missions WHERE id=?`, id).
		Scan(&m.ID, &m.WorkspaceID, &m.Title, &m.SquadID, &snap, &m.Status, &m.Attention, &at, &m.Brief, &binding.WorkspaceRoot, &binding.CanonicalRoot, &binding.PlanPath, &binding.PlanSlug, &binding.PlanDigest, &binding.DeliveryID, &binding.OpenedDigest)
	if err != nil {
		return m, err
	}
	if binding.PlanPath != "" {
		binding.WorkspaceID = m.WorkspaceID
		m.Binding = &binding
	}
	_ = json.Unmarshal([]byte(snap), &m.SquadSnapshot)
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return m, nil
}

func (s *Store) ListMissions(workspaceID string) ([]domain.Mission, error) {
	q := `SELECT id,workspace_id,title,squad_id,snapshot,status,attention,created_at,brief,binding_root,binding_canonical_root,plan_path,plan_slug,plan_digest,delivery_id,opened_digest FROM missions`
	args := []any{}
	if workspaceID != "" {
		q += ` WHERE workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Mission
	for rows.Next() {
		var m domain.Mission
		var snap, at string
		var binding domain.MissionBinding
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.Title, &m.SquadID, &snap, &m.Status, &m.Attention, &at, &m.Brief, &binding.WorkspaceRoot, &binding.CanonicalRoot, &binding.PlanPath, &binding.PlanSlug, &binding.PlanDigest, &binding.DeliveryID, &binding.OpenedDigest); err != nil {
			return nil, err
		}
		if binding.PlanPath != "" {
			binding.WorkspaceID = m.WorkspaceID
			m.Binding = &binding
		}
		_ = json.Unmarshal([]byte(snap), &m.SquadSnapshot)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, m)
	}
	if out == nil {
		out = []domain.Mission{}
	}
	return out, rows.Err()
}

func (s catalogWriter) PutAccount(a domain.Account) error {
	c := 0
	if a.Connected {
		c = 1
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO accounts(id,label,provider,adapter,isolation,connected,session_cap,auth_method,vault_ref) VALUES(?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Label, a.Provider, a.Adapter, string(a.Isolation), c, a.SessionCap, a.AuthMethod, a.VaultRef)
	return err
}

func (s *Store) GetAccount(id string) (domain.Account, error) {
	var a domain.Account
	var iso string
	var c int
	err := s.db.QueryRow(`SELECT id,label,provider,adapter,isolation,connected,session_cap,auth_method,vault_ref FROM accounts WHERE id=?`, id).
		Scan(&a.ID, &a.Label, &a.Provider, &a.Adapter, &iso, &c, &a.SessionCap, &a.AuthMethod, &a.VaultRef)
	if err != nil {
		return a, err
	}
	a.Isolation = domain.Isolation(iso)
	a.Connected = c == 1
	return a, nil
}

func (s *Store) ListAccounts() ([]domain.Account, error) {
	rows, err := s.db.Query(`SELECT id,label,provider,adapter,isolation,connected,session_cap,auth_method,vault_ref FROM accounts ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		var a domain.Account
		var iso string
		var c int
		if err := rows.Scan(&a.ID, &a.Label, &a.Provider, &a.Adapter, &iso, &c, &a.SessionCap, &a.AuthMethod, &a.VaultRef); err != nil {
			return nil, err
		}
		a.Isolation = domain.Isolation(iso)
		a.Connected = c == 1
		out = append(out, a)
	}
	if out == nil {
		out = []domain.Account{}
	}
	return out, rows.Err()
}

func (s *Store) CountRunningSessions(accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE account_id=? AND process='running'`, accountID).Scan(&n)
	return n, err
}

func (s catalogWriter) PutSession(sess domain.Session) error {
	argv, err := json.Marshal(sess.Argv)
	if err != nil {
		return err
	}
	batuta := 0
	if sess.BatutaInjected {
		batuta = 1
	}
	var ended any
	if sess.EndedAt != nil {
		ended = sess.EndedAt.Format(time.RFC3339Nano)
	}
	_, err = s.db.Exec(`INSERT INTO sessions(id,mission_id,workspace_id,member_id,account_id,adapter,argv,process,work,owner,owner_client,pid,cols,rows,batuta,skills_version,created_at,ended_at,process_identity,recovery_reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET mission_id=excluded.mission_id, workspace_id=excluded.workspace_id, member_id=excluded.member_id, account_id=excluded.account_id, adapter=excluded.adapter, argv=excluded.argv, process=excluded.process, work=excluded.work, owner=excluded.owner, owner_client=excluded.owner_client, pid=excluded.pid, cols=excluded.cols, rows=excluded.rows, batuta=excluded.batuta, skills_version=excluded.skills_version, created_at=excluded.created_at, ended_at=excluded.ended_at, process_identity=excluded.process_identity, recovery_reason=excluded.recovery_reason WHERE sessions.process <> 'exited'`,
		sess.ID, sess.MissionID, sess.WorkspaceID, sess.MemberID, sess.AccountID, sess.Adapter, string(argv), sess.Process, sess.Work, sess.Owner, sess.OwnerClient, sess.PID, sess.Cols, sess.Rows, batuta, sess.SkillsVersion, sess.CreatedAt.Format(time.RFC3339Nano), ended, sess.ProcessIdentity, sess.RecoveryReason)
	return err
}

func (s *Store) GetSession(id string) (domain.Session, error) {
	return scanSession(s.db.QueryRow(`SELECT id,mission_id,workspace_id,member_id,account_id,adapter,argv,process,work,owner,owner_client,pid,cols,rows,batuta,skills_version,created_at,ended_at,process_identity,recovery_reason FROM sessions WHERE id=?`, id))
}

func scanSession(row interface{ Scan(...any) error }) (domain.Session, error) {
	var sess domain.Session
	var argv, created string
	var ended sql.NullString
	var batuta int
	err := row.Scan(&sess.ID, &sess.MissionID, &sess.WorkspaceID, &sess.MemberID, &sess.AccountID, &sess.Adapter, &argv, &sess.Process, &sess.Work, &sess.Owner, &sess.OwnerClient, &sess.PID, &sess.Cols, &sess.Rows, &batuta, &sess.SkillsVersion, &created, &ended, &sess.ProcessIdentity, &sess.RecoveryReason)
	if err != nil {
		return sess, err
	}
	_ = json.Unmarshal([]byte(argv), &sess.Argv)
	sess.BatutaInjected = batuta == 1
	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if ended.Valid && ended.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, ended.String)
		sess.EndedAt = &t
	}
	return sess, nil
}

func (s *Store) ListSessions() ([]domain.Session, error) {
	rows, err := s.db.Query(`SELECT id,mission_id,workspace_id,member_id,account_id,adapter,argv,process,work,owner,owner_client,pid,cols,rows,batuta,skills_version,created_at,ended_at,process_identity,recovery_reason FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	if out == nil {
		out = []domain.Session{}
	}
	return out, rows.Err()
}

func (s catalogWriter) AppendEvent(kind string, payload map[string]any) (domain.Event, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, err
	}
	at := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO events(at,kind,payload) VALUES(?,?,?)`, at.Format(time.RFC3339Nano), kind, string(b))
	if err != nil {
		return domain.Event{}, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return domain.Event{}, err
	}
	return domain.Event{Seq: seq, At: at, Kind: kind, Payload: payload}, nil
}

func (s *Store) EventsSince(cursor int64, limit int) ([]domain.Event, error) {
	return s.EventsSinceContext(context.Background(), cursor, limit)
}

func (s *Store) EventsSinceContext(ctx context.Context, cursor int64, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,at,kind,payload FROM events WHERE seq>? ORDER BY seq LIMIT ?`, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var at, payload string
		if err := rows.Scan(&e.Seq, &at, &e.Kind, &payload); err != nil {
			return nil, err
		}
		e.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("decode event %d timestamp: %w", e.Seq, err)
		}
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return nil, fmt.Errorf("decode event %d payload: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	if out == nil {
		out = []domain.Event{}
	}
	return out, rows.Err()
}

func (s *Store) EventsRecent(limit int) ([]domain.Event, error) {
	return s.EventsRecentContext(context.Background(), limit)
}

func (s *Store) EventsRecentContext(ctx context.Context, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,at,kind,payload FROM events ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var at, payload string
		if err := rows.Scan(&e.Seq, &at, &e.Kind, &payload); err != nil {
			return nil, err
		}
		e.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("decode event %d timestamp: %w", e.Seq, err)
		}
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return nil, fmt.Errorf("decode event %d payload: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	if out == nil {
		out = []domain.Event{}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) LastEventSeq() (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

func IsNotFound(err error) bool {
	return err == sql.ErrNoRows || (err != nil && strings.Contains(err.Error(), "no rows"))
}

func Errf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// HasSessionStopRequest uses durable exact-session intent, not event recency or
// process termination as a proxy. Invalid/legacy payloads cannot prove intent.
func (s *Store) HasSessionStopRequest(id string) (bool, error) {
	var found bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM events WHERE kind='session.stopped' AND CASE WHEN json_valid(payload) THEN json_type(payload, '$.id')='text' AND json_extract(payload, '$.id')=? ELSE 0 END)`, id).Scan(&found)
	return found, err
}
