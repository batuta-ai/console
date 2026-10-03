package session

// ProcessObservation is read-only evidence of an incarnation, never ownership.
// Missing includes an observed zombie/dead process. An inspection error leaves
// both fields empty and must not be interpreted as absence.
type ProcessObservation struct {
	Identity string
	Missing  bool
}
