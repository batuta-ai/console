package main

import (
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
)

func nativeExecutorMode(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--native-executor" {
		return false, nil
	}
	if len(args) < 4 {
		return true, domain.ErrInvalidReference
	}
	return true, service.RunNativeExecutor(args[1], args[2], args[3:])
}
