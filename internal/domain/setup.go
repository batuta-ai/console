package domain

import "time"

// SetupConfig contains confirmed inputs, never inferred from the host identity.
type SetupConfig struct {
	Stack       string `json:"stack"`
	Methodology string `json:"methodology"`
	Test        string `json:"test"`
	Build       string `json:"build"`
	Install     string `json:"install"`
	Execution   string `json:"execution"`
	Worktree    string `json:"worktree"`
	Template    string `json:"template"`
}

type WorkspaceSetup struct {
	Ready             bool         `json:"ready"`
	State             string       `json:"state"`
	Code              string       `json:"code,omitempty"`
	ConfigurationCode string       `json:"configuration_code,omitempty"`
	Attempt           *InitAttempt `json:"attempt,omitempty"`
}

// StopRequested records accepted intent; neither it nor Process implies an exit code.
type InitAttempt struct {
	SessionID      string     `json:"session_id"`
	AccountID      string     `json:"account_id"`
	State          string     `json:"state"`
	Process        string     `json:"process"`
	StopRequested  bool       `json:"stop_requested"`
	RecoveryReason string     `json:"recovery_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
}
