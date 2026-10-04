package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

type ReadinessError struct{ Code, AccountID string }

func (e ReadinessError) Error() string {
	if e.AccountID != "" {
		return "account " + e.AccountID + " is not ready: " + e.Code
	}
	return "account is not ready: " + e.Code
}

func (s *Service) diagnosticProbe() adapters.DiagnosticProbe {
	if s.Session == nil {
		return nil
	}
	return func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
		r, err := s.Session.Probe(ctx, argv, env, dir)
		return r.Output, r.ExitCode, err
	}
}

func (s *Service) readinessChecker() *adapters.ReadinessChecker {
	if s.Readiness != nil {
		return s.Readiness
	}
	return adapters.NewReadinessChecker(s.diagnosticProbe(), nil)
}

func ReadinessFailure(err error) (string, bool) {
	var failure ReadinessError
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.Code, true
}

// Structural draft state is durable; authentication and provider availability
// are fresh observations used by validation and official starts only.
func (s *Service) validateStructure(q domain.Squad) (domain.Validation, error) {
	v := domain.ValidateSquad(q)
	if !v.Ready {
		return v, nil
	}
	for _, member := range q.Members {
		a, err := s.Store.GetAccount(member.AccountID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return domain.Validation{}, err
			}
			return domain.Validation{Tone: "warning", Title: "Validation: unknown account", Text: "Select an account from the catalog.", Ready: false}, nil
		}
		if a.Adapter != member.CLI {
			return domain.Validation{Tone: "warning", Title: "Validation: account adapter mismatch", Text: "Select an account for this member's CLI.", Ready: false}, nil
		}
		if member.CLI != "codex" && member.CLI != "claude" {
			return domain.Validation{Tone: "warning", Title: "Validation: adapter_unsupported", Text: "Select Codex or Claude.", Ready: false}, nil
		}
	}
	return v, nil
}
