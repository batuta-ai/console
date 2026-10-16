package session

import "golang.org/x/sys/windows"

func stopClosedHandleError() error { return windows.ERROR_INVALID_HANDLE }
