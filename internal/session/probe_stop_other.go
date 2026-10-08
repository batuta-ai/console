//go:build !windows

package session

func requestProbeStop(p *ownedProcess) error { return p.stop() }
