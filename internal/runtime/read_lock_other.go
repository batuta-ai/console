//go:build !windows

package runtime

import "os"

func readLockBytes(path string) ([]byte, error) { return os.ReadFile(path) }
