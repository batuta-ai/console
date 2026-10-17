package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// NEVER-SHIP. Observations have no authority and must be removed before release.
// No output queue or goroutine: synchronous stderr can block and perturb/mask the
// failure, including while p.mu is held. Failed/dropped streams cannot prove absence.
const consoleDecisionEnv = "BATUTA_NEVER_SHIP_NATIVE_CONSOLE_TRACE"
const consoleDecisionFrameLimit = 96
const consoleDecisionTraceLimit = 128
const consoleDecisionOutputLimit = 16 * 1024
const consoleDecisionLimits = "NEVER-SHIP; non-atomic observations; null is unknown; actual root handle only; late PID/birth observations cannot prove identity at accounting time; no diagnostic control authority; synchronous stderr/Win32 calls (including under owned lock) perturb timing and may mask failure; no wall-clock I/O bound; max 128 traces/process, 96 frames/trace, 16KiB/frame plus one bounded retire record/trace; cap exhaustion/delivery failure can drop all subsequent evidence; pre-final snapshot may precede cancellation; final-promoted proves branch entry; events before observer publication unavailable (first forced origin may be unknown); no absence/liveness conclusion"

// Process-start opt-in: Windows Getenv allocates and calls Win32 on every read.
// Never refresh it on the command path; disabled hooks only read nil observers.
var consoleDecisionEnabled = os.Getenv(consoleDecisionEnv) == "1"

var consoleDecisionCount atomic.Uint64

type consoleDecisionTrace struct {
	mu             sync.Mutex
	id             uint64
	sequence       uint32
	start          time.Time
	output         *os.File
	deliveryFailed bool
	firstForced    string
}

type consoleDecisionFrame struct {
	Limits           string
	ObserverPID      int
	TraceStartUTC    string
	TraceID          uint64
	Sequence         uint32
	Event            string
	OffsetNS         int64
	Error            string
	Cleanup          string
	Wait             string
	ContextError     string
	ContextCause     string
	WaitStatus       *uint32
	KernelCode       *uint32
	Forced           *bool
	Exited           *bool
	FirstForced      string
	AccountingActive *uint32
	AccountingTotal  *uint32
	Written          *int
	Expected         *int
	CanceledNonzero  *bool
	Promotion        *bool
	PublicExit       *int
	Root             *graceDecisionProcess
}

func consoleDecisionValue[T any](v T) *T { return &v }

func consoleDecisionError(err error) string { return graceDecisionError(err) }

func formatConsoleDecision(r consoleDecisionFrame) ([]byte, error) {
	r.Limits = consoleDecisionLimits
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(data)+len("CONSOLE-DECISION \n") > consoleDecisionOutputLimit {
		return nil, fmt.Errorf("diagnostic frame exceeds %d bytes", consoleDecisionOutputLimit)
	}
	return append(append([]byte("CONSOLE-DECISION "), data...), '\n'), nil
}

func (d *consoleDecisionTrace) emit(r consoleDecisionFrame) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sequence >= consoleDecisionFrameLimit || d.deliveryFailed {
		return
	}
	d.sequence++
	r.TraceID, r.Sequence = d.id, d.sequence
	r.ObserverPID = os.Getpid()
	r.TraceStartUTC = d.start.UTC().Format(time.RFC3339Nano)
	if !d.start.IsZero() {
		r.OffsetNS = time.Since(d.start).Nanoseconds()
	}
	r.FirstForced = d.firstForced
	if d.sequence == consoleDecisionFrameLimit {
		r.Event = "record-limit"
	}
	data, err := formatConsoleDecision(r)
	if err == nil {
		var n int
		n, err = d.output.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		d.deliveryFailed = true
		// One bounded stderr-only delivery notice. A failed stream may lose this too.
		notice, _ := formatConsoleDecision(consoleDecisionFrame{TraceID: d.id, Sequence: d.sequence, Event: "delivery-unavailable", Error: consoleDecisionError(err)})
		_, _ = d.output.Write(notice)
	}
}

func beginConsoleDecision(p *ownedProcess) {
	if !consoleDecisionEnabled {
		return
	}
	id := consoleDecisionCount.Add(1)
	if id > consoleDecisionTraceLimit {
		return
	}
	d := &consoleDecisionTrace{id: id, start: time.Now(), output: os.Stderr}
	p.mu.Lock()
	root := inspectGraceDecisionProcess(graceDecisionProcess{Handle: p.root, Errors: make(map[string]string)}, p.job, d.start)
	d.emit(consoleDecisionFrame{Event: "begin", Root: &root, Forced: &p.forced, Exited: &p.exited})
	// Readers include hooks already holding p.mu; atomic publication avoids
	// recursive locking and makes the complete begin frame precede later frames.
	p.decision.Store(d)
	p.mu.Unlock()
}

func consoleDecisionEvent(p *ownedProcess, event string, err error) {
	if p == nil {
		return
	}
	d := p.decision.Load()
	if d == nil {
		return
	}
	d.emit(consoleDecisionFrame{Event: event, Error: consoleDecisionError(err)})
}

// Wrappers return precisely the original error, without joining observer errors.
func consoleDecisionResult(p *ownedProcess, event string, err error) error {
	consoleDecisionEvent(p, event, err)
	return err
}

func consoleDecisionForced(p *ownedProcess, origin string) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	d.mu.Lock()
	if !p.forced && d.firstForced == "" {
		d.firstForced = origin
	}
	d.mu.Unlock()
	d.emit(consoleDecisionFrame{Event: "before-forced-" + origin, Forced: &p.forced})
}

func consoleDecisionRootWait(p *ownedProcess, status uint32, err error) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	r := consoleDecisionFrame{Event: "actual-root-wait", Error: consoleDecisionError(err)}
	if err == nil {
		r.WaitStatus = consoleDecisionValue(status)
	}
	d.emit(r)
}

func consoleDecisionCode(p *ownedProcess, err error) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	r := consoleDecisionFrame{Event: "actual-root-code", Error: consoleDecisionError(err), Exited: &p.exited, Forced: &p.forced}
	if err == nil {
		code := p.exitCode
		r.KernelCode = consoleDecisionValue(code)
	}
	d.emit(r)
}

func consoleDecisionAccounting(p *ownedProcess, a windowsJobAccounting, err error) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	r := consoleDecisionFrame{Event: "original-retire-accounting", Error: consoleDecisionError(err), Forced: &p.forced, Exited: &p.exited}
	if err == nil {
		r.AccountingActive = consoleDecisionValue(a.ActiveProcesses)
		r.AccountingTotal = consoleDecisionValue(a.TotalProcesses)
	}
	d.emit(r)
	traceGraceDecisionDiagnostic(p, a, err)
}

func consoleDecisionOutcome(p *ownedProcess, cleanup, wait error) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	d.emit(consoleDecisionFrame{Event: "wait-outcome", Cleanup: consoleDecisionError(cleanup), Wait: consoleDecisionError(wait)})
}

func consoleDecisionInjection(p *ownedProcess, written, expected int, err error) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	d.emit(consoleDecisionFrame{Event: "injection", Written: consoleDecisionValue(written), Expected: consoleDecisionValue(expected), Error: consoleDecisionError(err)})
}

func consoleDecisionFinal(p *ownedProcess, ctx context.Context, forced bool, code uint32) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	canceledNonzero := ctx.Err() != nil && code != 0
	promotion := forced || canceledNonzero
	r := consoleDecisionFrame{Event: "pre-final-predicate-snapshot", Forced: consoleDecisionValue(forced), Exited: &p.exited, ContextError: consoleDecisionError(ctx.Err()), ContextCause: consoleDecisionError(context.Cause(ctx)), CanceledNonzero: consoleDecisionValue(canceledNonzero), Promotion: consoleDecisionValue(promotion)}
	if p.exited {
		r.KernelCode = consoleDecisionValue(code)
	}
	d.emit(r)
}

func consoleDecisionReturn(p *ownedProcess, ctx context.Context, exit int, err error) {
	if p == nil {
		return
	}
	d := p.decision.Load()
	if d == nil {
		return
	}
	d.emit(consoleDecisionFrame{Event: "return", PublicExit: consoleDecisionValue(exit), Error: consoleDecisionError(err), ContextError: consoleDecisionError(ctx.Err()), ContextCause: consoleDecisionError(context.Cause(ctx))})
}

func consoleDecisionCanceled(p *ownedProcess, ctx context.Context) {
	d := p.decision.Load()
	if d == nil {
		return
	}
	consoleDecisionEvent(p, "outer-canceled", context.Cause(ctx))
}
