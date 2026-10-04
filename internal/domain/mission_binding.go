package domain

import "errors"

// MissionBinding pins a catalog association to a core plan and, optionally, a
// verified journal incarnation. The root prevents a moved workspace reference
// from changing the meaning of an existing association.
type MissionBinding struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceRoot string `json:"workspace_root"`
	CanonicalRoot string `json:"canonical_root"`
	PlanPath      string `json:"plan_path"`
	PlanSlug      string `json:"plan_slug"`
	PlanDigest    string `json:"plan_digest"`
	DeliveryID    string `json:"delivery_id,omitempty"`
	OpenedDigest  string `json:"opened_digest,omitempty"`
}

var ErrBindingConflict = errors.New("mission binding conflicts with existing ownership")
