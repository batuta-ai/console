//go:build !windows

package accounts

import (
	"errors"
	"os"
	"path/filepath"
)

func environmentKey(key string) string               { return key }
func platformRuntimeKey(string) bool                 { return false }
func platformIdentityKey(string) bool                { return false }
func platformIdentityPaths(string) map[string]string { return nil }
func platformHomeMetadata(string) []string           { return nil }

func newSessionHome() (string, error) {
	home, err := os.MkdirTemp("", "batuta-console-session-")
	if err != nil {
		return "", err
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(home))
	}
	return canonicalHome, nil
}

func createIdentityDirectory(path string) error { return os.MkdirAll(path, 0700) }
