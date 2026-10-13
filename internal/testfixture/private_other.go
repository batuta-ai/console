//go:build !windows

package testfixture

import "testing"

func PrivateHome(t testing.TB) string {
	t.Helper()
	return t.TempDir()
}
