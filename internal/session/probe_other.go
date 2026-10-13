//go:build !darwin && !linux && !windows

package session

import "context"

func (m *Manager) Probe(context.Context, []string, []string, string) (ProbeResult, error) {
	return ProbeResult{ExitCode: -1}, ErrProbeUnsupported
}
