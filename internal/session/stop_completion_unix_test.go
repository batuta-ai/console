//go:build unix

package session

import "os"

func stopClosedHandleError() error { return os.ErrClosed }
