//go:build !windows

package api

import (
	"os"
	"testing"
)

type artifactPermissions struct{ path string }

func newArtifactPermissions(t *testing.T, path string) *artifactPermissions {
	t.Helper()
	return &artifactPermissions{path: path}
}

func (p *artifactPermissions) chmod(perm os.FileMode) error {
	return os.Chmod(p.path, perm)
}
