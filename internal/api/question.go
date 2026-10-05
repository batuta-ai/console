package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var req domain.AnswerRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAnswer(w, 400, domain.AnswerResult{Effect: domain.AnswerRejected, Error: &domain.AnswerFailure{Code: "answer_request", Message: "invalid answer request"}})
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		writeAnswer(w, 400, domain.AnswerResult{Target: req.Identity(), Effect: domain.AnswerRejected, Error: &domain.AnswerFailure{Code: "answer_request", Message: "invalid answer request"}})
		return
	}
	result, err := s.Svc.AnswerQuestion(r.Context(), req)
	status := http.StatusOK
	if err != nil {
		var code string
		status, code = answerError(result, err)
		result.Error = &domain.AnswerFailure{Code: code, Message: err.Error()}
	}
	writeAnswer(w, status, result)
}

func answerError(result domain.AnswerResult, err error) (int, string) {
	switch {
	case result.Effect == domain.AnswerRecorded:
		return 500, "answer_recorded_error"
	case result.Effect == domain.AnswerUncertain:
		return 503, "answer_uncertain"
	case errors.Is(err, loop.ErrInvalidQuestionTarget):
		return 400, "answer_target"
	case errors.Is(err, domain.ErrInvalidReference):
		return 400, "answer_reference"
	case errors.Is(err, loop.ErrStaleQuestion):
		return 409, "answer_stale"
	case errors.Is(err, loop.ErrDeliveryOwned):
		return 409, "answer_owned"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 408, "answer_canceled"
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, batuta.ErrArtifactMissing):
		return 404, "answer_missing"
	case errors.Is(err, batuta.ErrArtifactInvalid):
		return 422, "answer_artifact"
	case errors.Is(err, batuta.ErrArtifactIO):
		return 503, "answer_io"
	default:
		return 500, "answer_store"
	}
}

func writeAnswer(w http.ResponseWriter, status int, result domain.AnswerResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
