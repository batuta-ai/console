package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

// NEVER SHIP: remove this file and all neverShipInit anchors together.
// No callbacks, result references, cancellation functions, or process handles
// cross this boundary. Only a bounded value channel reaches the test reporter.
const neverShipInitRecordLimit = 64

type neverShipInitTraceKey struct{}

type neverShipInitTrace struct {
	id      int
	start   time.Time
	records chan<- neverShipInitRecord
	busy    atomic.Bool
	lost    atomic.Bool
	count   int
}

type neverShipInitRecord struct {
	RequestID   int    `json:"request_id"`
	Sequence    int    `json:"sequence"`
	Phase       string `json:"phase"`
	ElapsedNS   int64  `json:"elapsed_ns"`
	RemainingNS *int64 `json:"remaining_ns"`
	ContextErr  string `json:"ctx_err"`
	Cause       string `json:"cause"`
	ErrorClass  string `json:"error_class"`
	ExitCode    *int   `json:"exit_code"`
}

func neverShipInitReadinessResult(ctx context.Context, r domain.AccountReadiness) {
	var err error
	if !r.Ready {
		err = ReadinessError{Code: r.Code}
	}
	neverShipInitObserve(ctx, "readiness_exit", err, -1)
}

func neverShipInitObserve(ctx context.Context, phase string, err error, exit int) {
	trace, _ := ctx.Value(neverShipInitTraceKey{}).(*neverShipInitTrace)
	if trace == nil || trace.id < 1 || trace.id > 10 || !neverShipInitPhaseAllowed(phase) {
		return
	}
	if !trace.busy.CompareAndSwap(false, true) {
		trace.lost.Store(true)
		return
	}
	defer trace.busy.Store(false)
	if trace.count >= neverShipInitRecordLimit {
		trace.lost.Store(true)
		return
	}
	trace.count++
	now := time.Now()
	r := neverShipInitRecord{
		RequestID: trace.id, Sequence: trace.count, Phase: phase,
		ElapsedNS:  now.Sub(trace.start).Nanoseconds(),
		ContextErr: neverShipInitErrorClass(ctx.Err()),
		Cause:      neverShipInitErrorClass(context.Cause(ctx)), ErrorClass: neverShipInitErrorClass(err),
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := deadline.Sub(now).Nanoseconds()
		r.RemainingNS = &remaining
	}
	// Manager.Probe uses -1 for an unavailable exit status, including cancellation.
	if phase == "probe_exit" && exit >= 0 {
		r.ExitCode = &exit
	}
	select {
	case trace.records <- r:
	default:
		trace.lost.Store(true)
	}
}

func neverShipInitPhaseAllowed(phase string) bool {
	switch phase {
	case "validation_enter", "validation_exit", "init_cli_enter", "init_cli_exit",
		"readiness_enter", "readiness_exit", "probe_enter", "probe_exit",
		"provider_transport_enter", "provider_transport_exit", "reuse_wait", "reuse_acquired",
		"skills_enter", "skills_exit", "spawn_persist_enter", "spawn_persist_exit", "returned":
		return true
	}
	return false
}

func neverShipInitErrorClass(err error) string {
	// Do not call Error, Is, As or Unwrap: error implementations can execute code
	// or expose credentials. Unknown and wrapped errors deliberately stay coarse.
	switch err {
	case nil:
		return "none"
	case context.Canceled:
		return "cancelled"
	case context.DeadlineExceeded:
		return "deadline"
	}
	switch e := err.(type) {
	case catalogReferenceError:
		return "catalog"
	case ReadinessError:
		switch e.Code {
		case "check_cancelled", "authenticated_account_required", "adapter_unsupported",
			"platform_unsupported", "cli_missing", "account_disconnected", "isolation_invalid",
			"probe_unavailable", "cli_probe_failed", "cli_incompatible", "authentication_required",
			"credential_missing", "credential_invalid", "auth_method_unsupported",
			"provider_unavailable", "provider_redirect", "provider_rejected", "provider_forbidden",
			"provider_response_invalid":
			return e.Code
		}
		return "readiness_other"
	}
	return "other"
}
