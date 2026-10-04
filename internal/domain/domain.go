package domain

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}

func Now() time.Time { return time.Now().UTC() }

type Isolation string

const (
	IsolationSupported       Isolation = "supported"
	IsolationUnsupported     Isolation = "unsupported"
	IsolationNotYetQualified Isolation = "not_yet_qualified"
)

type SourceState string

const (
	SourceCurrent     SourceState = "current"
	SourceStale       SourceState = "stale"
	SourceUnavailable SourceState = "unavailable"
	SourceUnsupported SourceState = "unsupported"
	SourceUnknown     SourceState = "unknown"
)

type Workspace struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Root      string         `json:"root"`
	CreatedAt time.Time      `json:"created_at"`
	Profile   bool           `json:"profile"`
	Setup     WorkspaceSetup `json:"setup"`
}

type Member struct {
	ID        string   `json:"id"`
	Role      string   `json:"role"`
	CLI       string   `json:"cli"`
	Model     string   `json:"model,omitempty"`
	Effort    string   `json:"effort,omitempty"`
	AccountID string   `json:"account_id"`
	Skills    []string `json:"skills,omitempty"`
	Maestro   bool     `json:"maestro"`
}

const (
	OriginBatuta = "batuta"
	OriginCustom = "custom"
)

type Squad struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Purpose string   `json:"purpose"`
	Version int      `json:"version"`
	Draft   bool     `json:"draft"` // Structural catalog completeness, not authentication.
	Origin  string   `json:"origin"`
	Members []Member `json:"members"`
}

type Mission struct {
	Binding       *MissionBinding `json:"binding,omitempty"`
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	Title         string          `json:"title"`
	Brief         string          `json:"brief,omitempty"`
	SquadID       string          `json:"squad_id"`
	SquadSnapshot Squad           `json:"squad_snapshot"`
	Status        string          `json:"status"`
	Attention     string          `json:"attention,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// PlanRef is a Batuta plan file already on disk. The Console reads it; it does not write the plan.
type PlanRef struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	Done   bool   `json:"done,omitempty"`
}

type Account struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	Provider   string            `json:"provider"`
	Adapter    string            `json:"adapter"`
	Isolation  Isolation         `json:"isolation"`
	Connected  bool              `json:"connected"`
	Authed     bool              `json:"authed"`
	SessionCap int               `json:"session_cap"`
	AuthMethod string            `json:"auth_method"`
	VaultRef   string            `json:"vault_ref,omitempty"`
	Readiness  *AccountReadiness `json:"readiness,omitempty"`
}

type AccountReadiness struct {
	Supported            bool      `json:"supported"`
	Available            bool      `json:"available"`
	CLICompatible        bool      `json:"cli_compatible"`
	Connected            bool      `json:"connected"`
	CredentialConfigured bool      `json:"credential_configured"`
	Authenticated        bool      `json:"authenticated"`
	Ready                bool      `json:"ready"`
	Code                 string    `json:"code"`
	CredentialSource     string    `json:"credential_source,omitempty"`
	CheckedAt            time.Time `json:"checked_at"`
}

type Session struct {
	ID              string     `json:"id"`
	MissionID       string     `json:"mission_id"`
	WorkspaceID     string     `json:"workspace_id"`
	MemberID        string     `json:"member_id,omitempty"`
	AccountID       string     `json:"account_id,omitempty"`
	Adapter         string     `json:"adapter"`
	Argv            []string   `json:"argv"`
	Process         string     `json:"process"` // running, exited, unknown
	Work            string     `json:"work"`
	Owner           string     `json:"owner"` // tui, web, none
	OwnerClient     string     `json:"owner_client,omitempty"`
	ProcessIdentity string     `json:"process_identity,omitempty"`
	RecoveryReason  string     `json:"recovery_reason,omitempty"`
	PID             int        `json:"pid,omitempty"`
	Cols            int        `json:"cols"`
	Rows            int        `json:"rows"`
	BatutaInjected  bool       `json:"batuta_injected"`
	SkillsVersion   string     `json:"skills_version,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
}

type Event struct {
	Seq     int64          `json:"seq"`
	At      time.Time      `json:"at"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

type LimitSnapshot struct {
	AccountID   string      `json:"account_id"`
	Label       string      `json:"label"`
	Window      string      `json:"window"`
	Scope       string      `json:"scope,omitempty"`
	Percent     *float64    `json:"percent"`
	ResetAt     *time.Time  `json:"reset_at,omitempty"`
	ObservedAt  *time.Time  `json:"observed_at,omitempty"`
	LastAttempt time.Time   `json:"last_attempt"`
	Source      SourceState `json:"source"`
	Origin      string      `json:"origin"`
}

type Validation struct {
	Code  string `json:"code,omitempty"`
	Tone  string `json:"tone"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Ready bool   `json:"ready"`
}

type WatchView struct {
	MissionID   string           `json:"mission_id,omitempty"`
	WorkspaceID string           `json:"workspace_id,omitempty"`
	Scope       string           `json:"scope"`
	Source      SourceState      `json:"source"`
	API         string           `json:"api"`
	Workspace   string           `json:"workspace"`
	Delivery    string           `json:"delivery,omitempty"`
	Header      map[string]any   `json:"header"`
	Attention   map[string]any   `json:"attention"`
	Context     map[string]any   `json:"context"`
	Progress    map[string]any   `json:"progress"`
	Detail      map[string]any   `json:"detail"`
	Waves       []map[string]any `json:"waves"`
	Health      map[string]any   `json:"health"`
	LogTitle    string           `json:"log_title,omitempty"`
	LogLines    []string         `json:"log_lines,omitempty"`
	Plans       []PlanRef        `json:"plans,omitempty"`
	Available   bool             `json:"available"`
	Message     string           `json:"message,omitempty"`
}

func ValidateSquad(s Squad) Validation {
	if len(s.Members) == 0 {
		return Validation{Tone: "danger", Title: "Validation: empty squad", Text: "Add at least one member and a maestro. No model was invoked.", Ready: false}
	}
	maestro := 0
	incomplete := 0
	unqualified := 0
	for _, m := range s.Members {
		if m.Maestro {
			maestro++
		}
		if m.Role == "" || m.CLI == "" || m.AccountID == "" {
			incomplete++
		}
	}
	if maestro == 0 {
		return Validation{Tone: "warning", Title: "Validation: no maestro", Text: "Name which member conducts the mission.", Ready: false}
	}
	if incomplete > 0 {
		return Validation{Tone: "warning", Title: fmt.Sprintf("Validation: %d incomplete member(s)", incomplete), Text: "Role, CLI and account are required. Model may inherit from the squad.", Ready: false}
	}
	if unqualified > 0 {
		return Validation{Tone: "warning", Title: "Validation: isolation pending", Text: "An account is not yet qualified for isolation. Validate probes capabilities; no paid task started.", Ready: false}
	}
	return Validation{Tone: "success", Title: "Validation: ready to save", Text: "Capabilities and accounts checked. Saving is a catalog write; it does not call a model.", Ready: true}
}

// ResolveBinding applies workspace > mission override > member > squad default.
// Empty fields inherit from the next lower layer.
func ResolveBinding(workspace, mission, member, squad Member) Member {
	out := squad
	overlay := func(src Member) {
		if src.CLI != "" {
			out.CLI = src.CLI
		}
		if src.Model != "" {
			out.Model = src.Model
		}
		if src.Effort != "" {
			out.Effort = src.Effort
		}
		if src.AccountID != "" {
			out.AccountID = src.AccountID
		}
		if src.Role != "" {
			out.Role = src.Role
		}
		if len(src.Skills) > 0 {
			out.Skills = src.Skills
		}
	}
	overlay(member)
	overlay(mission)
	overlay(workspace)
	return out
}

// ErrInvalidReference distinguishes incompatible input from catalog failures.
var ErrInvalidReference = errors.New("invalid catalog reference")

// SelectSnapshotMember keeps historical membership authoritative. Generic sessions
// still require a mission/workspace; only explicit injection opt-out permits an
// empty snapshot. Configuration inheritance must never change this selected ID.
func SelectSnapshotMember(snapshot Squad, id string, inject bool) (Member, error) {
	if len(snapshot.Members) == 0 {
		if id != "" {
			return Member{}, fmt.Errorf("member %q: %w", id, sql.ErrNoRows)
		}
		if inject {
			return Member{}, fmt.Errorf("%w: injection requires a snapshot member", ErrInvalidReference)
		}
		return Member{}, nil
	}
	seen := make(map[string]bool)
	selected := snapshot.Members[0]
	found := false
	for _, member := range snapshot.Members {
		if member.ID == "" || seen[member.ID] {
			return Member{}, fmt.Errorf("%w: snapshot member IDs must be nonempty and unique", ErrInvalidReference)
		}
		seen[member.ID] = true
		if id != "" && member.ID == id {
			selected, found = member, true
		} else if id == "" && member.Maestro {
			selected = member
		}
	}
	if id != "" && !found {
		return Member{}, fmt.Errorf("member %q: %w", id, sql.ErrNoRows)
	}
	return selected, nil
}
