//go:build !windows

package service

import (
	"os"
	"testing"
)

func credentialRecordInput(t *testing.T) *os.File {
	t.Helper()
	return os.Stdin
}
