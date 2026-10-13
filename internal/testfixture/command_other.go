//go:build !windows

package testfixture

import "testing"

func Argv(t testing.TB, original []string) []string {
	t.Helper()
	return original
}

func TerminalEnvironment(t testing.TB, original []string) []string {
	t.Helper()
	return original
}
