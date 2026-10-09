package main

import (
	"context"
	"os"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
)

func nativeCoreMode(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--native-core" {
		return false, nil
	}
	if len(args) != 1 {
		return true, domain.ErrInvalidReference
	}
	return true, service.RunNativeCore(context.Background(), os.Stdin, os.Stdout)
}
