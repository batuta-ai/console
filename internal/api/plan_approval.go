package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
)

// Only catalog workspace IDs are accepted; no raw-root or discovery fallback.
func planWorkspace(r *http.Request) (string, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", domain.ErrInvalidReference
	}
	if len(q) != 1 || len(q["workspace_id"]) != 1 || q.Get("workspace_id") == "" {
		err = domain.ErrInvalidReference
	}
	return q.Get("workspace_id"), err
}

func (s *Server) planDetail(w http.ResponseWriter, r *http.Request) {
	workspace, err := planWorkspace(r)
	if err != nil {
		writePlanError(w, err)
		return
	}
	detail, err := s.Svc.PlanDetail(r.Context(), r.PathValue("id"), workspace)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, detail)
}

func (s *Server) planApproval(w http.ResponseWriter, r *http.Request) {
	workspace, err := planWorkspace(r)
	if err != nil {
		writePlanError(w, err)
		return
	}
	receipt, err := s.Svc.PlanApproval(r.Context(), r.PathValue("id"), workspace, r.PathValue("receipt"))
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, receipt)
}

func (s *Server) approvePlan(w http.ResponseWriter, r *http.Request) {
	result := domain.PlanApprovalResult{Effect: domain.PlanApprovalRejected}
	var identity domain.PlanIdentity
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&identity)
	result.Target = identity
	if err != nil {
		writePlanResult(w, 400, result, "plan_request", "invalid approval request")
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		writePlanResult(w, 400, result, "plan_request", "invalid approval request")
		return
	}
	if identity.MissionID != r.PathValue("id") || r.URL.RawQuery != "" {
		writePlanResult(w, 400, result, "plan_reference", "invalid mission or query")
		return
	}
	receipt, err := s.Svc.ApprovePlan(r.Context(), identity)
	if err != nil {
		status, code := planErrorStatus(err)
		if errors.Is(err, domain.ErrPlanApprovalUncertain) {
			result.Effect = domain.PlanApprovalUncertain
		}
		writePlanResult(w, status, result, code, err.Error())
		return
	}
	result.Effect = domain.PlanApprovalRecorded
	result.Receipt = &receipt
	writePlanResult(w, 200, result, "", "")
}

func planErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrPlanApprovalUncertain):
		return 503, "plan_uncertain"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 408, "plan_canceled"
	case errors.Is(err, domain.ErrInvalidReference):
		return 400, "plan_reference"
	case errors.Is(err, domain.ErrPlanStale):
		return 409, "plan_stale"
	case errors.Is(err, domain.ErrBindingConflict):
		return 409, "plan_binding"
	case errors.Is(err, domain.ErrPlanTransition):
		return 409, "plan_transition"
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, batuta.ErrArtifactMissing):
		return 404, "plan_missing"
	case errors.Is(err, batuta.ErrArtifactInvalid):
		return 422, "plan_artifact"
	case errors.Is(err, batuta.ErrArtifactIO):
		return 503, "plan_io"
	default:
		return 500, "plan_store"
	}
}

func writePlanError(w http.ResponseWriter, err error) {
	status, code := planErrorStatus(err)
	writeErr(w, status, code, err.Error())
}

func writePlanResult(w http.ResponseWriter, status int, result domain.PlanApprovalResult, code, message string) {
	if code != "" {
		result.Error = &domain.PlanFailure{Code: code, Message: message}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
