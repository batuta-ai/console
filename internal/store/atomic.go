package store

import (
	"database/sql"
	"fmt"

	"github.com/batuta-ai/console/internal/domain"
)

type sqlExecutor interface {
	Exec(string, ...any) (sql.Result, error)
}

type catalogWriter struct{ db sqlExecutor }

func (s *Store) withEvent(kind string, payload map[string]any, put func(catalogWriter) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("%s begin: %w", kind, err)
	}
	defer tx.Rollback()
	writer := catalogWriter{db: tx}
	if err := put(writer); err != nil {
		return fmt.Errorf("%s mutation: %w", kind, err)
	}
	if _, err := writer.AppendEvent(kind, payload); err != nil {
		return fmt.Errorf("%s event: %w", kind, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s commit: %w", kind, err)
	}
	return nil
}

func (s *Store) AppendEvent(kind string, payload map[string]any) (domain.Event, error) {
	return (catalogWriter{db: s.db}).AppendEvent(kind, payload)
}

func (s *Store) PutWorkspace(value domain.Workspace) error {
	return (catalogWriter{db: s.db}).PutWorkspace(value)
}

// PutWorkspaceWithEvent commits the entity and its explicit event together.
func (s *Store) PutWorkspaceWithEvent(value domain.Workspace, kind string, payload map[string]any) error {
	return s.withEvent(kind, payload, func(w catalogWriter) error { return w.PutWorkspace(value) })
}

func (s *Store) PutSquad(value domain.Squad) error {
	return (catalogWriter{db: s.db}).PutSquad(value)
}

// PutSquadWithEvent commits the entity and its explicit event together.
func (s *Store) PutSquadWithEvent(value domain.Squad, kind string, payload map[string]any) error {
	return s.withEvent(kind, payload, func(w catalogWriter) error { return w.PutSquad(value) })
}

func (s *Store) PutMission(value domain.Mission) error {
	return (catalogWriter{db: s.db}).PutMission(value)
}

// PutMissionWithEvent commits the entity and its explicit event together.
func (s *Store) PutMissionWithEvent(value domain.Mission, kind string, payload map[string]any) error {
	return s.withEvent(kind, payload, func(w catalogWriter) error { return w.PutMission(value) })
}

func (s *Store) PutAccount(value domain.Account) error {
	return (catalogWriter{db: s.db}).PutAccount(value)
}

// PutAccountWithEvent commits the entity and its explicit event together.
func (s *Store) PutAccountWithEvent(value domain.Account, kind string, payload map[string]any) error {
	return s.withEvent(kind, payload, func(w catalogWriter) error { return w.PutAccount(value) })
}

func (s *Store) PutSession(value domain.Session) error {
	return (catalogWriter{db: s.db}).PutSession(value)
}

// PutSessionWithEvent commits the entity and its explicit event together.
func (s *Store) PutSessionWithEvent(value domain.Session, kind string, payload map[string]any) error {
	return s.withEvent(kind, payload, func(w catalogWriter) error { return w.PutSession(value) })
}
