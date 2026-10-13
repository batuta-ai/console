//go:build !windows

package session

import "testing"

func sessionTestArgv(t *testing.T, original []string) []string {
	t.Helper()
	return original
}
