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
	Target       PlanIdentity    `json:"target"`
	ApprovalID   string          `json:"approval_id"`
	Effect       ExecutionEffect `json:"effect"`
	Delivery     string          `json:"delivery,omitempty"`
	OpenedDigest string          `json:"opened_digest,omitempty"`
	Error        *PlanFailure    `json:"error,omitempty"`
}

type ExecutionState struct {
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
