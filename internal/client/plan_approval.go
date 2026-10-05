package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
)

// PlanError preserves transport uncertainty and server error codes. No approval
// is automatically retried. Refresh detail to reconcile a lost mutation reply.
type PlanError struct {
	Status  int
	Code    string
	Message string
	Effect  domain.PlanApprovalEffect
	Cause   error
}

func (e *PlanError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("plan: %v", e.Cause)
	}
	return e.Code + ": " + e.Message
}

func (e *PlanError) Unwrap() error { return e.Cause }

func planPath(mission string) (string, error) {
	if mission == "" || mission == "." || mission == ".." || strings.ContainsAny(mission, "/\\") {
		return "", domain.ErrInvalidReference
	}
	return "/v0/missions/" + url.PathEscape(mission) + "/plan", nil
}

func (c *Client) planRequest(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Do not expose a replayable body to the transport.
	req.GetBody = nil
	response, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	const limit = 16 << 20
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if len(payload) > limit {
		return nil, response.StatusCode, fmt.Errorf("plan response exceeds limit")
	}
	return payload, response.StatusCode, nil
}

func decodePlan(payload []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("invalid trailing plan response")
	}
	return nil
}

func readPlanError(status int, body []byte) error {
	var failure struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := decodePlan(body, &failure); err != nil {
		return &PlanError{Status: status, Cause: err}
	}
	return &PlanError{Status: status, Code: failure.Error, Message: failure.Message}
}

func validReceipt(a domain.PlanApproval) bool {
	return a.ID != "" && !a.ApprovedAt.IsZero() && a.Identity.Validate() == nil && len(a.Content) > 0 && len(a.Content) <= domain.MaxPlanBytes && domain.PlanVersion(a.Content) == a.Identity.Version
}

func (c *Client) PlanDetail(ctx context.Context, mission, workspace string) (domain.PlanDetail, error) {
	var out domain.PlanDetail
	path, err := planPath(mission)
	if err != nil || workspace == "" {
		return out, &PlanError{Cause: domain.ErrInvalidReference}
	}
	body, status, err := c.planRequest(ctx, http.MethodGet, path+"?workspace_id="+url.QueryEscape(workspace), nil)
	if err != nil {
		return out, &PlanError{Status: status, Cause: err}
	}
	if status != 200 {
		return out, readPlanError(status, body)
	}
	if err := decodePlan(body, &out); err != nil {
		return domain.PlanDetail{}, &PlanError{Status: status, Cause: err}
	}
	if out.Identity.Validate() != nil || out.Identity.MissionID != mission || out.Identity.Binding.WorkspaceID != workspace || len(out.Content) == 0 || len(out.Content) > domain.MaxPlanBytes || domain.PlanVersion(out.Content) != out.Identity.Version || (out.Approval != nil && (!validReceipt(*out.Approval) || out.Approval.Identity != out.Identity)) {
		return domain.PlanDetail{}, &PlanError{Status: status, Cause: fmt.Errorf("invalid plan detail identity or content")}
	}
	return out, nil
}

func (c *Client) PlanApproval(ctx context.Context, mission, workspace, receipt string) (domain.PlanApproval, error) {
	var out domain.PlanApproval
	path, err := planPath(mission)
	if err != nil || workspace == "" || receipt == "" || receipt == "." || receipt == ".." || strings.ContainsAny(receipt, "/\\") {
		return out, &PlanError{Cause: domain.ErrInvalidReference}
	}
	body, status, err := c.planRequest(ctx, http.MethodGet, path+"/approvals/"+url.PathEscape(receipt)+"?workspace_id="+url.QueryEscape(workspace), nil)
	if err != nil {
		return out, &PlanError{Status: status, Cause: err}
	}
	if status != 200 {
		return out, readPlanError(status, body)
	}
	if err := decodePlan(body, &out); err != nil {
		return domain.PlanApproval{}, &PlanError{Status: status, Cause: err}
	}
	if !validReceipt(out) || out.ID != receipt || out.Identity.MissionID != mission || out.Identity.Binding.WorkspaceID != workspace {
		return domain.PlanApproval{}, &PlanError{Status: status, Cause: fmt.Errorf("invalid plan receipt identity or content")}
	}
	return out, nil
}

func (c *Client) ApprovePlan(ctx context.Context, identity domain.PlanIdentity) (domain.PlanApprovalResult, error) {
	result := domain.PlanApprovalResult{Target: identity, Effect: domain.PlanApprovalRejected}
	fail := func(status int, code, message string, cause error) (domain.PlanApprovalResult, error) {
		return result, &PlanError{Status: status, Code: code, Message: message, Effect: result.Effect, Cause: cause}
	}
	if err := ctx.Err(); err != nil {
		return fail(0, "", "", err)
	}
	if err := identity.Validate(); err != nil {
		return fail(0, "", "", err)
	}
	path, err := planPath(identity.MissionID)
	if err != nil {
		return fail(0, "", "", err)
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return fail(0, "", "", err)
	}
	result.Effect = domain.PlanApprovalUncertain
	body, status, err := c.planRequest(ctx, http.MethodPost, path+"/approvals", payload)
	if status == 401 || status == 403 {
		result.Effect = domain.PlanApprovalRejected
		return fail(status, "plan_unauthorized", "approval authorization failed", nil)
	}
	if err != nil {
		return fail(status, "", "", err)
	}
	var received domain.PlanApprovalResult
	if err := decodePlan(body, &received); err != nil {
		return fail(status, "", "", err)
	}
	valid := received.Target == identity
	switch received.Effect {
	case domain.PlanApprovalRecorded:
		valid = valid && status == 200 && received.Error == nil && received.Receipt != nil && validReceipt(*received.Receipt) && received.Receipt.Identity == identity
	case domain.PlanApprovalRejected, domain.PlanApprovalUncertain:
		valid = valid && status >= 400 && received.Error != nil && received.Error.Code != "" && received.Receipt == nil
	default:
		valid = false
	}
	if !valid {
		return fail(status, "", "", fmt.Errorf("invalid plan approval identity or effect"))
	}
	result = received
	if received.Error != nil {
		return fail(status, received.Error.Code, received.Error.Message, nil)
	}
	return result, nil
}
