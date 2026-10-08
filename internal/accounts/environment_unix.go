//go:build !windows

package accounts

import "os"

func environmentKey(key string) string               { return key }
func platformRuntimeKey(string) bool                 { return false }
func platformIdentityKey(string) bool                { return false }
func platformIdentityPaths(string) map[string]string { return nil }
func platformHomeMetadata(string) []string           { return nil }
func newSessionHome() (string, error)                { return os.MkdirTemp("", "batuta-console-session-") }
func createIdentityDirectory(path string) error      { return os.MkdirAll(path, 0700) }
