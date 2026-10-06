package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

func (s *Server) startExecution(w http.ResponseWriter, r *http.Request) {
	var req domain.ExecutionRequest
	result := domain.ExecutionResult{Effect: domain.ExecutionRejected}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	err := d.Decode(&req)
	result.Target, result.ApprovalID = req.Target, req.ApprovalID
	if err != nil || d.Decode(new(json.RawMessage)) != io.EOF {
		writeExecutionResult(w, 400, result, "execution_request", "invalid execution request")
		return
	}
	if req.Target.MissionID != r.PathValue("id") || r.URL.RawQuery != "" {
		writeExecutionResult(w, 400, result, "execution_reference", "invalid mission or query")
		return
	}
	result, err = s.Svc.StartExecution(r.Context(), req)
	if err != nil {
		status, code := executionErrorStatus(err)
		writeExecutionResult(w, status, result, code, err.Error())
		return
	}
	writeExecutionResult(w, http.StatusAccepted, result, "", "")
}

func (s *Server) executionState(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["workspace_id"]) != 1 || len(q["approval_id"]) != 1 || q.Get("workspace_id") == "" || q.Get("approval_id") == "" {
		writeErr(w, 400, "execution_reference", "exact workspace and approval IDs required")
		return
	}
	state, err := s.Svc.ExecutionState(r.Context(), r.PathValue("id"), q.Get("workspace_id"), q.Get("approval_id"))
	if err != nil {
		status, code := executionErrorStatus(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, state)
}

func (s *Server) resumeExecution(w http.ResponseWriter, r *http.Request) {
	var req domain.ResumeRequest
	result := domain.ExecutionResult{Effect: domain.ExecutionRejected}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	err := d.Decode(&req)
	result.Target, result.ApprovalID = req.Target, req.ApprovalID
	if err != nil || d.Decode(new(json.RawMessage)) != io.EOF {
		writeExecutionResult(w, 400, result, "execution_request", "invalid resume request")
		return
	}
	if req.Target.MissionID != r.PathValue("id") || r.URL.RawQuery != "" {
		writeExecutionResult(w, 400, result, "execution_reference", "invalid mission or query")
		return
	}
	result, err = s.Svc.ResumeExecution(r.Context(), req)
	if err != nil {
		status, code := executionErrorStatus(err)
		writeExecutionResult(w, status, result, code, err.Error())
		return
	}
	writeExecutionResult(w, http.StatusAccepted, result, "", "")
}

func executionErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, loop.ErrDeliveryExists), errors.Is(err, loop.ErrDeliveryOwned):
		return 409, "execution_owned"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		if errors.Is(err, domain.ErrExecutionUncertain) {
			return 503, "execution_uncertain"
		}
		return 408, "execution_canceled"
	case errors.Is(err, domain.ErrExecutionApprovalRequired):
		return 409, "execution_approval_required"
	case errors.Is(err, domain.ErrExecutionExists):
		return 409, "execution_exists"
	case errors.Is(err, domain.ErrExecutionUnavailable):
		return 503, "execution_unavailable"
	case errors.Is(err, domain.ErrExecutionClosing):
		return 503, "execution_closing"
	case errors.Is(err, domain.ErrExecutionUncertain):
		return 503, "execution_uncertain"
	default:
		return planErrorStatus(err)
	}
}

func writeExecutionResult(w http.ResponseWriter, status int, result domain.ExecutionResult, code, message string) {
	if code != "" {
		result.Error = &domain.PlanFailure{Code: code, Message: message}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
