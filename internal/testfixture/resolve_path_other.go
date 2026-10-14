//go:build !windows

package testfixture

import "path/filepath"

func ResolvePath(path string) (string, error) { return filepath.EvalSymlinks(path) }
