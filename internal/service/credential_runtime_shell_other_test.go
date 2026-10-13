//go:build !windows

package service

import "testing"

func credentialRuntimeShell(t *testing.T) string {
	t.Helper()
	return "/bin/sh"
}
