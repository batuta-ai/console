//go:build !windows

package main

func nativeCoreMode([]string) (bool, error) { return false, nil }
