package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

func (s *Service) AnswerQuestion(ctx context.Context, req domain.AnswerRequest) (domain.AnswerResult, error) {
	result := domain.AnswerResult{Target: req.Identity(), Effect: domain.AnswerRejected}
	if err := batuta.ValidateAnswer(req); err != nil {
		return result, err
	}
	if req.Workspace != "" && req.Workspace != req.WorkspaceID {
		return result, domain.ErrInvalidReference
	}
	m, ws, err := s.missionWorkspace(req.MissionID, req.WorkspaceID)
	if err != nil {
		return result, err
	}
	b := m.Binding
	if b == nil || b.WorkspaceID != ws.ID || b.WorkspaceRoot != ws.Root || b.DeliveryID != req.Delivery || b.OpenedDigest != req.OpenedDigest {
		return result, domain.ErrInvalidReference
	}
	canonical, err := batuta.CanonicalWorkspace(ws.Root)
	if err != nil {
		return result, err
	}
	if canonical != b.CanonicalRoot {
		return result, domain.ErrInvalidReference
	}
	// Unlike WatchMission, mutation preflight must preserve artifact failures.
	if _, err := s.projectMission(ctx, m.ID, ws.ID, *b); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	id, err := batuta.Answer(ctx, canonical, req)
	result.Delivery = id
	switch {
	case id != "":
		result.Effect = domain.AnswerRecorded
	case err == nil:
		result.Effect = domain.AnswerUncertain
	case errors.Is(err, loop.ErrInvalidQuestionTarget), errors.Is(err, loop.ErrStaleQuestion), errors.Is(err, loop.ErrDeliveryOwned), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
	default:
		result.Effect = domain.AnswerUncertain
	}
	if id != "" {
		_, eventErr := s.Store.AppendEvent("watch.answered", map[string]any{"mission_id": req.MissionID, "workspace_id": req.WorkspaceID, "delivery": id, "task": req.Task, "execution": req.Execution, "question_id": req.QuestionID, "opened_digest": req.OpenedDigest})
		if eventErr != nil {
			err = errors.Join(err, fmt.Errorf("answer recorded for delivery %s, but catalog event failed: %w", id, eventErr))
		}
	}
	return result, err
}
