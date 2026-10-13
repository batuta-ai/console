//go:build !windows

package service

func credentialSessionCacheKey() string { return "TMPDIR" }
