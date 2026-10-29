//go:build !windows

package batuta

import "path/filepath"

func canonicalWorkspacePath(path string) (string, error) { return filepath.EvalSymlinks(path) }
