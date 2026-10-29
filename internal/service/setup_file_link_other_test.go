//go:build !windows

package service

import "testing"

func setupFileLinkWorkspace(t *testing.T) string {
	t.Helper()
	return ""
}
