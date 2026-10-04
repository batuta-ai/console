package session

import "errors"

var (
	ErrInvalidProbe     = errors.New("diagnostic requires a command")
	ErrProbeClosing     = errors.New("session manager is shutting down")
	ErrProbeExit        = errors.New("diagnostic exited unsuccessfully")
	ErrProbeOutputLimit = errors.New("diagnostic output exceeds limit")
	ErrProbeUnsupported = errors.New("diagnostic processes unsupported on this platform")
)

// ProbeResult contains stdout only after successful execution and cleanup.
// Callers must not persist credential-bearing output in session transcripts.
type ProbeResult struct {
	Output   []byte
	ExitCode int
}
