package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
)

type ExecutionError struct {
	Result domain.ExecutionResult
	Status int
	Cause  error
}

func (e *ExecutionError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("execution: %v", e.Cause)
	}
	if e.Result.Error != nil {
		return e.Result.Error.Code + ": " + e.Result.Error.Message
	}
	return fmt.Sprintf("execution: HTTP %d", e.Status)
}
func (e *ExecutionError) Unwrap() error { return e.Cause }

func executionPath(mission string) (string, error) {
	path, err := planPath(mission)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, "/plan") + "/execution", nil
}

func validOpening(value string) bool {
	value, ok := strings.CutPrefix(value, "sha256:")
	if !ok || value != strings.ToLower(value) {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32
}

func (c *Client) mutateExecution(ctx context.Context, req domain.ExecutionRequest, resume *domain.ResumeRequest) (domain.ExecutionResult, error) {
	result := domain.ExecutionResult{Target: req.Target, ApprovalID: req.ApprovalID, Effect: domain.ExecutionRejected}
	fail := func(status int, err error) (domain.ExecutionResult, error) {
		return result, &ExecutionError{Result: result, Status: status, Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(0, err)
	}
	if err := req.Target.Validate(); err != nil {
		return fail(0, err)
	}
	if req.ApprovalID == "" {
		return fail(0, domain.ErrExecutionApprovalRequired)
	}
	path, err := executionPath(req.Target.MissionID)
	if err != nil {
		return fail(0, err)
	}
	var payload []byte
	if resume != nil {
		if resume.Delivery != domain.ExecutionDelivery(req.ApprovalID, req.Target) || !validOpening(resume.OpenedDigest) {
			return fail(0, domain.ErrInvalidReference)
		}
		path += "/resume"
		payload, err = json.Marshal(resume)
	} else {
		payload, err = json.Marshal(req)
	}
	if err != nil {
		return fail(0, err)
	}
	result.Effect = domain.ExecutionUncertain
	body, status, err := c.planRequest(ctx, http.MethodPost, path, payload)
	if status == 401 || status == 403 {
		result.Effect = domain.ExecutionRejected
		result.Error = &domain.PlanFailure{Code: "execution_unauthorized", Message: "execution authorization failed"}
		return fail(status, err)
	}
	if err != nil {
		return fail(status, err)
	}
	var received domain.ExecutionResult
	if err := decodePlan(body, &received); err != nil {
		return fail(status, err)
	}
	valid := received.Target == req.Target && received.ApprovalID == req.ApprovalID
	delivery := domain.ExecutionDelivery(req.ApprovalID, req.Target)
	switch received.Effect {
	case domain.ExecutionStarted:
		valid = valid && status == 202 && received.Error == nil && received.Delivery == delivery && validOpening(received.OpenedDigest)
		if resume != nil {
			valid = valid && received.OpenedDigest == resume.OpenedDigest
		}
	case domain.ExecutionRejected:
		valid = valid && status >= 400 && received.Error != nil && received.Error.Code != "" && received.Delivery == "" && received.OpenedDigest == ""
	case domain.ExecutionUncertain:
		valid = valid && status >= 400 && received.Error != nil && received.Error.Code != "" && (received.Delivery == "" || received.Delivery == delivery) && (received.OpenedDigest == "" || validOpening(received.OpenedDigest))
	default:
		valid = false
	}
	if !valid {
		return fail(status, fmt.Errorf("invalid native execution identity or effect"))
	}
	result = received
	if received.Error != nil {
		return fail(status, nil)
	}
	return result, nil
}

func (c *Client) StartExecution(ctx context.Context, req domain.ExecutionRequest) (domain.ExecutionResult, error) {
	return c.mutateExecution(ctx, req, nil)
}

func (c *Client) ResumeExecution(ctx context.Context, req domain.ResumeRequest) (domain.ExecutionResult, error) {
	return c.mutateExecution(ctx, domain.ExecutionRequest{Target: req.Target, ApprovalID: req.ApprovalID}, &req)
}

func (c *Client) ExecutionState(ctx context.Context, mission, workspace, approvalID string) (domain.ExecutionState, error) {
	var out domain.ExecutionState
	path, err := executionPath(mission)
	if err != nil || workspace == "" || approvalID == "" {
		return out, &ExecutionError{Cause: domain.ErrInvalidReference}
	}
	query := url.Values{"workspace_id": {workspace}, "approval_id": {approvalID}}
	body, status, err := c.planRequest(ctx, http.MethodGet, path+"?"+query.Encode(), nil)
	if err != nil {
		return out, &ExecutionError{Status: status, Cause: err}
	}
	if status != 200 {
		return out, &ExecutionError{Status: status, Cause: readPlanError(status, body)}
	}
	if err := decodePlan(body, &out); err != nil {
		return domain.ExecutionState{}, &ExecutionError{Status: status, Cause: err}
	}
	valid := out.Target.Validate() == nil && out.Target.MissionID == mission && out.Target.Binding.WorkspaceID == workspace && out.Target.Binding.DeliveryID == "" && out.ApprovalID == approvalID && out.Delivery == domain.ExecutionDelivery(approvalID, out.Target)
	valid = valid && validExecutionProjection(out, mission, workspace)
	if out.Watch != nil && out.Watch.PendingQuestion != nil {
		q := out.Watch.PendingQuestion.Target
		valid = valid && q.MissionID == mission && q.WorkspaceID == workspace && q.Delivery == out.Delivery && q.OpenedDigest == out.OpenedDigest
	}
	if !valid {
		return domain.ExecutionState{}, &ExecutionError{Status: status, Cause: fmt.Errorf("invalid native execution state identity")}
	}
	return out, nil
}

func validExecutionProjection(state domain.ExecutionState, mission, workspace string) bool {
	if state.OpenedDigest == "" {
		return state.Watch == nil && !state.Bound && state.Gate == nil
	}
	view, gate := state.Watch, state.Gate
	if !validOpening(state.OpenedDigest) || view == nil || gate == nil || view.Progression == nil {
		return false
	}
	if view.Delivery != state.Delivery || view.MissionID != mission || view.WorkspaceID != workspace || view.Source != domain.SourceCurrent {
		return false
	}
	if *gate != *view.Progression || !gate.Required || gate.Delivery != state.Delivery {
		return false
	}
	if gate.EvidenceDigest != "" && !validOpening(gate.EvidenceDigest) {
		return false
	}
	if gate.Cleared && (gate.ReviewID == "" || !validOpening(gate.EvidenceDigest)) {
		return false
	}
	return view.Header["state"] != "done" || gate.Cleared
}
