package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const MaxPlanBytes = 1 << 20

var (
	ErrPlanStale      = errors.New("plan content differs from the shown version")
	ErrPlanTransition = errors.New("plan is not eligible for initial execution approval")
)

// Version hashes every source byte; Binding.PlanDigest remains Core's task-set identity.
type PlanIdentity struct {
	MissionID string         `json:"mission_id"`
	Binding   MissionBinding `json:"binding"`
	Version   string         `json:"version"`
}

func PlanVersion(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (p PlanIdentity) Validate() error {
	b := p.Binding
	digest, err := hex.DecodeString(p.Version)
	if err != nil || len(digest) != sha256.Size || p.Version != strings.ToLower(p.Version) || p.MissionID == "" || b.WorkspaceID == "" || b.WorkspaceRoot == "" || b.CanonicalRoot == "" || b.PlanPath == "" || b.PlanSlug == "" || b.PlanDigest == "" || (b.DeliveryID == "") != (b.OpenedDigest == "") {
		return ErrInvalidReference
	}
	return nil
}

type PlanTask struct {
	ID           string   `json:"id"`
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Domain       string   `json:"domain"`
	Complexity   string   `json:"complexity"`
	Dependencies []string `json:"dependencies"`
	Executor     string   `json:"executor"`
	Model        string   `json:"model"`
	Scope        []string `json:"scope"`
	Accept       []string `json:"accept"`
	Line         int      `json:"line"`
}

type PlanDetail struct {
	Identity PlanIdentity `json:"identity"`
	// JSON encodes bytes as base64 so invalid UTF-8 and line endings remain lossless.
	Content  []byte        `json:"content"`
	Title    string        `json:"title"`
	Goal     string        `json:"goal"`
	Context  string        `json:"context"`
	Created  string        `json:"created"`
	Inputs   string        `json:"inputs"`
	Status   string        `json:"status"`
	Tasks    []PlanTask    `json:"tasks"`
	Approval *PlanApproval `json:"approval,omitempty"`
}

// PlanApproval is Console evidence, never a native execution or source-status claim.
type PlanApproval struct {
	ID         string       `json:"id"`
	Identity   PlanIdentity `json:"identity"`
	Content    []byte       `json:"content"`
	ApprovedAt time.Time    `json:"approved_at"`
}

var ErrPlanApprovalUncertain = errors.New("plan approval commit outcome is uncertain")

type PlanApprovalEffect string

const (
	PlanApprovalRecorded  PlanApprovalEffect = "recorded"
	PlanApprovalRejected  PlanApprovalEffect = "rejected"
	PlanApprovalUncertain PlanApprovalEffect = "uncertain"
)

type PlanFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PlanApprovalResult struct {
	Target  PlanIdentity       `json:"target"`
	Effect  PlanApprovalEffect `json:"effect"`
	Receipt *PlanApproval      `json:"receipt,omitempty"`
	Error   *PlanFailure       `json:"error,omitempty"`
}
