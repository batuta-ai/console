//go:build !windows

package service

import (
	"context"
	"sync/atomic"

	"github.com/batuta-ai/console/internal/domain"
)

func (s *Service) runNativeWindowsExecution(*nativeExecution, domain.ExecutionRequest, domain.PlanApproval, string, nativeAccounts, string, []string, *atomic.Bool, func(context.Context) error) (bool, string, error) {
	return false, "", nil
}
