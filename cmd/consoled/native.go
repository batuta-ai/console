package main

import (
	"context"
	"errors"
	"github.com/batuta-ai/console/internal/service"
)

type daemonOptions struct{ CoreExecutable string }

func drainExecution(ctx context.Context, svc *service.Service) error {
	if svc.Session != nil {
		svc.Session.CloseAdmission()
	}
	nativeErr := svc.ShutdownNative(ctx)
	var sessionErr error
	if svc.Session != nil {
		sessionErr = svc.Session.Shutdown(ctx)
	}
	return errors.Join(nativeErr, sessionErr)
}
