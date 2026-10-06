//go:build !darwin && !linux

package service

import "github.com/batuta-ai/console/internal/session"

func execNativeBridge(string, []string, []string) error { return session.ErrProbeUnsupported }
