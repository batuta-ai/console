package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

func ExecutionDelivery(approvalID string, identity PlanIdentity) string {
	sum := sha256.Sum256([]byte(approvalID + "\x00" + identity.MissionID + "\x00" + identity.Binding.WorkspaceID))
	return "console-" + hex.EncodeToString(sum[:])
}

var (
	ErrExecutionApprovalRequired = errors.New("execution requires the exact approved receipt")
	ErrExecutionExists           = errors.New("mission already has a native delivery; resume it explicitly")
	ErrExecutionUnavailable      = errors.New("native execution is unavailable")
	ErrExecutionClosing          = errors.New("daemon is shutting down")
	ErrExecutionUncertain        = errors.New("native execution outcome is uncertain; inspect the exact delivery before retrying")
)

type ExecutionRequest struct {
	Target     PlanIdentity `json:"target"`
	ApprovalID string       `json:"approval_id"`
}

type ResumeRequest struct {
	Target       PlanIdentity `json:"target"`
	ApprovalID   string       `json:"approval_id"`
	Delivery     string       `json:"delivery"`
	OpenedDigest string       `json:"opened_digest"`
}

type ExecutionEffect string

const (
	ExecutionStarted   ExecutionEffect = "started"
	ExecutionRejected  ExecutionEffect = "rejected"
	ExecutionUncertain ExecutionEffect = "uncertain"
)

type ExecutionResult struct {
	RunID        string          `json:"run_id,omitempty"`
	Target       PlanIdentity    `json:"target"`
	ApprovalID   string          `json:"approval_id"`
	Effect       ExecutionEffect `json:"effect"`
	Delivery     string          `json:"delivery,omitempty"`
	OpenedDigest string          `json:"opened_digest,omitempty"`
	Error        *PlanFailure    `json:"error,omitempty"`
}

type ExecutionState struct {
	RunID        string         `json:"run_id,omitempty"`
	Gate         *ExecutionGate `json:"gate,omitempty"`
	Target       PlanIdentity   `json:"target"`
	ApprovalID   string         `json:"approval_id"`
	Delivery     string         `json:"delivery"`
	OpenedDigest string         `json:"opened_digest,omitempty"`
	Active       bool           `json:"active"`
	Bound        bool           `json:"bound"`
	Watch        *WatchView     `json:"watch,omitempty"`
}

type ExecutionGate struct {
	Required       bool   `json:"required"`
	Cleared        bool   `json:"cleared"`
	Delivery       string `json:"delivery"`
	ReviewID       string `json:"review_id,omitempty"`
	EvidenceDigest string `json:"evidence_digest,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// ExecutionStopRequest names one daemon admission, including its preparation.
// RunID is never reused by resume or daemon recreation.
type ExecutionStopRequest struct {
	Target     PlanIdentity `json:"target"`
	ApprovalID string       `json:"approval_id"`
	Delivery   string       `json:"delivery"`
	RunID      string       `json:"run_id"`
}

func ValidExecutionRunID(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}

func (r ExecutionStopRequest) Validate() error {
	if r.Target.Validate() != nil || r.Target.Binding.DeliveryID != "" || r.ApprovalID == "" || r.Delivery != ExecutionDelivery(r.ApprovalID, r.Target) || !ValidExecutionRunID(r.RunID) {
		return ErrInvalidReference
	}
	return nil
}

type ExecutionStopEffect string

const (
	ExecutionStopRejected  ExecutionStopEffect = "rejected"
	ExecutionStopPending   ExecutionStopEffect = "pending"
	ExecutionStopCompleted ExecutionStopEffect = "completed"
	ExecutionStopUncertain ExecutionStopEffect = "uncertain"
)

// Completed describes verified finalization, not a synthesized Core state.
// Causes preserves run/cancellation errors even when durability is verified.
type ExecutionStopResult struct {
	ExecutionStopRequest
	Effect    ExecutionStopEffect `json:"effect"`
	Finalized bool                `json:"finalized"`
	Durable   bool                `json:"durable"`
	CoreState string              `json:"core_state,omitempty"`
	State     *ExecutionState     `json:"state,omitempty"`
	Causes    []string            `json:"causes,omitempty"`
	Error     *PlanFailure        `json:"error,omitempty"`
}
