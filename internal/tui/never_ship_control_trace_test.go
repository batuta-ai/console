package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// NEVER-SHIP. Read once at process start: disabled fixtures return their exact
// original handler, dialer and RoundTripper, with no per-request observer work.
var neverShipControlEnabled = os.Getenv("BATUTA_NEVER_SHIP_CONTROL_TRACE") == "1"
var neverShipControlFixtureID atomic.Uint64

const (
	neverShipControlHeader   = "X-Batuta-Never-Ship-Control-Trace"
	neverShipControlRoute    = "/v0/sessions/actual-login/control"
	neverShipControlRequests = 4
	neverShipControlRecords  = 48
	neverShipControlOutput   = 12 << 10
	neverShipControlStack    = 64 << 10
)

type neverShipControlTrace struct {
	mu                      sync.Mutex
	fixture                 string
	out                     io.Writer
	traces                  [neverShipControlRequests]*neverShipControlRequest
	count, dropped          int
	closed                  bool
	outputMu                sync.Mutex
	delivered, droppedBytes int
	outputError             string
}

type neverShipControlEvent struct {
	stage, err string
	offset     int64
	value      int
}
type neverShipControlRequest struct {
	mu                                    sync.Mutex
	observer                              *neverShipControlTrace
	id                                    string
	start                                 time.Time
	ctx                                   context.Context
	events                                [neverShipControlRecords]neverShipControlEvent
	count, dropped                        int
	clientPhase, handlerPhase             string
	expiryClientPhase, expiryHandlerPhase string
	expiryHandlerActive                   bool
	expiryError                           string
	expiryOffset                          int64
	transportError, readError, closeError string
	handlerID                             uint64
	handlerActive                         bool
	handlerSeen                           bool
	deadline                              int64
	deadlineKnown                         bool
	stack                                 string
	stackFound, stackTruncated            bool
	stop, joined                          chan struct{}
	finishOnce                            sync.Once
}
type neverShipControlContextKey struct{}

func registerNeverShipControlTrace(t *testing.T, fixture string) *neverShipControlTrace {
	d := newNeverShipControlTrace(neverShipControlEnabled, fixture, os.Stderr)
	if d != nil {
		t.Cleanup(d.cleanup)
	}
	return d
}
func newNeverShipControlTrace(enabled bool, fixture string, out io.Writer) *neverShipControlTrace {
	if !enabled {
		return nil
	}
	// Only fixed synthetic fixture labels may reach stderr, never caller data.
	if fixture != "codex" && fixture != "claude" {
		fixture = "unknown"
	}
	return &neverShipControlTrace{fixture: fmt.Sprintf("%s-%d", fixture, neverShipControlFixtureID.Add(1)), out: out, outputError: "none"}
}
func neverShipControlMatches(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == neverShipControlRoute
}

func (d *neverShipControlTrace) handler(next http.Handler) http.Handler {
	if d == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !neverShipControlMatches(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Private correlation only; no auth, ownership or request-body inspection.
		id := r.Header.Get(neverShipControlHeader)
		d.mu.Lock()
		var trace *neverShipControlRequest
		for _, candidate := range d.traces {
			if candidate != nil && candidate.id == id {
				trace = candidate
				break
			}
		}
		d.mu.Unlock()
		if trace == nil {
			next.ServeHTTP(w, r)
			return
		}
		trace.mu.Lock()
		trace.handlerSeen, trace.handlerActive, trace.handlerID = true, true, neverShipCurrentGoroutine()
		trace.mu.Unlock()
		trace.record("handler_enter", nil, 0)
		defer func() {
			trace.record("handler_return", nil, 0)
			trace.mu.Lock()
			trace.handlerActive = false
			trace.mu.Unlock()
		}()
		next.ServeHTTP(&neverShipControlWriter{ResponseWriter: w, trace: trace}, r)
	})
}

type neverShipControlWriter struct {
	http.ResponseWriter
	trace *neverShipControlRequest
}

func (w *neverShipControlWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *neverShipControlWriter) WriteHeader(code int) {
	w.trace.record("response_header_enter", nil, code)
	w.ResponseWriter.WriteHeader(code)
	w.trace.record("response_header_return", nil, code)
}
func (w *neverShipControlWriter) Write(p []byte) (int, error) {
	w.trace.record("response_write_enter", nil, len(p))
	n, err := w.ResponseWriter.Write(p)
	w.trace.record("response_write_return", err, n)
	return n, err
}

func (d *neverShipControlTrace) dial(next func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if d == nil {
		return next
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		trace, _ := ctx.Value(neverShipControlContextKey{}).(*neverShipControlRequest)
		if trace == nil {
			return next(ctx, network, address)
		}
		trace.record("dial_enter", nil, 0)
		conn, err := next(ctx, network, address)
		trace.record("dial_return", err, 0)
		return conn, err
	}
}
func (d *neverShipControlTrace) transport(next http.RoundTripper) http.RoundTripper {
	if d == nil {
		return next
	}
	return &neverShipControlTransport{next: next, observer: d}
}

type neverShipControlTransport struct {
	next     http.RoundTripper
	observer *neverShipControlTrace
}

func (rt *neverShipControlTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !neverShipControlMatches(r) {
		return rt.next.RoundTrip(r)
	}
	d := rt.observer
	d.mu.Lock()
	if d.closed || d.count == len(d.traces) {
		d.dropped++
		d.mu.Unlock()
		return rt.next.RoundTrip(r)
	}
	trace := &neverShipControlRequest{observer: d, id: fmt.Sprintf("%s-%d", d.fixture, d.count+1), start: time.Now(), ctx: r.Context(), stop: make(chan struct{}), joined: make(chan struct{}), clientPhase: "unknown", handlerPhase: "unknown", expiryClientPhase: "unknown", expiryHandlerPhase: "unknown", expiryError: "unobserved", transportError: "unobserved", readError: "unobserved", closeError: "unobserved"}
	if deadline, ok := r.Context().Deadline(); ok {
		trace.deadlineKnown = true
		trace.deadline = deadline.Sub(trace.start).Microseconds()
	}
	d.traces[d.count] = trace
	d.count++
	d.mu.Unlock()
	trace.record("client_entry", nil, 0)
	hooks := &httptrace.ClientTrace{
		GetConn: func(string) { trace.record("get_conn", nil, 0) },
		GotConn: func(info httptrace.GotConnInfo) {
			reused := 0
			if info.Reused {
				reused = 1
			}
			trace.record("got_conn", nil, reused)
		},
		WroteHeaders:         func() { trace.record("request_headers_written", nil, 0) },
		WroteRequest:         func(info httptrace.WroteRequestInfo) { trace.record("request_written", info.Err, 0) },
		GotFirstResponseByte: func() { trace.record("first_response_byte", nil, 0) },
	}
	// Clone only the request metadata; do not read or replace its body. The private
	// header and httptrace callbacks perturb timing; they are not an API contract.
	traced := r.Clone(httptrace.WithClientTrace(context.WithValue(r.Context(), neverShipControlContextKey{}, trace), hooks))
	traced.Header.Set(neverShipControlHeader, trace.id)
	go trace.watch()
	response, err := rt.next.RoundTrip(traced)
	trace.record("transport_return", err, 0)
	if err != nil || response == nil || response.Body == nil {
		trace.finish()
		return response, err
	}
	response.Body = &neverShipControlBody{ReadCloser: response.Body, trace: trace}
	return response, err
}

type neverShipControlBody struct {
	io.ReadCloser
	trace *neverShipControlRequest
}

func (b *neverShipControlBody) Read(p []byte) (int, error) {
	b.trace.record("body_read_enter", nil, 0)
	n, err := b.ReadCloser.Read(p)
	b.trace.record("body_read_return", err, n)
	return n, err
}
func (b *neverShipControlBody) Close() error {
	b.trace.record("body_close_enter", nil, 0)
	err := b.ReadCloser.Close()
	b.trace.record("body_close_return", err, 0)
	b.trace.finish()
	return err
}
func neverShipControlError(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	default:
		return "other"
	}
}
func (r *neverShipControlRequest) record(stage string, err error, value int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.HasPrefix(stage, "handler_") || strings.HasPrefix(stage, "response_") {
		r.handlerPhase = stage
	} else if stage != "ctx_done" {
		r.clientPhase = stage
	}
	switch stage {
	case "transport_return":
		r.transportError = neverShipControlError(err)
	case "body_read_return":
		r.readError = neverShipControlError(err)
	case "body_close_return":
		r.closeError = neverShipControlError(err)
	}
	if r.count == len(r.events) {
		r.dropped++
		return
	}
	r.events[r.count] = neverShipControlEvent{stage: stage, err: neverShipControlError(err), value: value, offset: time.Since(r.start).Microseconds()}
	r.count++
}
func (r *neverShipControlRequest) watch() {
	defer close(r.joined)
	select {
	case <-r.ctx.Done():
	case <-r.stop:
	}
	// Recheck after completion wins the select, so simultaneous expiry is retained.
	if err := r.ctx.Err(); err != nil {
		r.mu.Lock()
		r.expiryClientPhase, r.expiryHandlerPhase, r.expiryHandlerActive = r.clientPhase, r.handlerPhase, r.handlerActive
		r.expiryError, r.expiryOffset = neverShipControlError(err), time.Since(r.start).Microseconds()
		r.mu.Unlock()
		r.record("ctx_done", err, 0)
		r.captureStack()
		r.observer.emit(r, "expiry")
	}
}
func (r *neverShipControlRequest) finish() { r.finishOnce.Do(func() { close(r.stop) }); <-r.joined }

func neverShipCurrentGoroutine() uint64 {
	var raw [64]byte
	n := runtime.Stack(raw[:], false)
	fields := bytes.Fields(raw[:n])
	if len(fields) < 2 {
		return 0
	}
	id, _ := strconv.ParseUint(string(fields[1]), 10, 64)
	return id
}
func (r *neverShipControlRequest) captureStack() {
	r.mu.Lock()
	id, active := r.handlerID, r.handlerActive
	r.mu.Unlock()
	if id == 0 || !active {
		return
	}
	// A single bounded all-goroutine sample, filtered to the correlated handler.
	// Never print raw arguments, source paths, other goroutines or environment.
	raw := make([]byte, neverShipControlStack)
	n := runtime.Stack(raw, true)
	prefix := []byte(fmt.Sprintf("goroutine %d ", id))
	var selected []byte
	for _, block := range bytes.Split(raw[:n], []byte("\n\n")) {
		if bytes.HasPrefix(block, prefix) {
			selected = block
			break
		}
	}
	var safe strings.Builder
	frames := 0
	truncated := n == len(raw)
	for _, line := range bytes.Split(selected, []byte("\n")) {
		if len(line) == 0 || line[0] == '\t' {
			continue
		}
		text := string(line)
		end := strings.LastIndexByte(text, '(')
		if end < 0 {
			continue
		}
		symbol := text[:end]
		allowed := false
		for _, prefix := range []string{"github.com/batuta-ai/console/internal/api.", "github.com/batuta-ai/console/internal/session.", "github.com/batuta-ai/console/internal/store.", "github.com/batuta-ai/console/internal/tui.", "net/http.", "net.", "sync.", "internal/sync.", "runtime.", "database/sql.", "modernc.org/sqlite."} {
			if strings.HasPrefix(symbol, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		if frames == 16 || len(symbol) > 200 {
			truncated = true
			continue
		}
		fmt.Fprintf(&safe, "stack_frame=%s\n", symbol)
		frames++
	}
	r.mu.Lock()
	r.stack, r.stackFound, r.stackTruncated = safe.String(), selected != nil, truncated
	r.mu.Unlock()
}

func (d *neverShipControlTrace) emit(r *neverShipControlRequest, reason string) {
	d.outputMu.Lock()
	defer d.outputMu.Unlock()
	r.mu.Lock()
	var out strings.Builder
	joined := false
	select {
	case <-r.joined:
		joined = true
	default:
	}
	found := "missing_or_inactive"
	if r.stackFound {
		found = "found"
	}
	fmt.Fprintf(&out, "NEVER-SHIP control_trace=%s snapshot=%s route=POST_actual-login_control non_atomic=true timing_perturbation=true decode=unobservable control_return=unobservable historical_cause=unknown ctx_observation=watcher_scheduled\n", r.id, reason)
	fmt.Fprintf(&out, "client_phase=%s handler_phase=%s handler_seen=%t handler_active=%t handler_join=unproven watcher_joined=%t deadline_known=%t deadline_offset_us=%d stack_handler=%s stack_truncated=%t\n", r.clientPhase, r.handlerPhase, r.handlerSeen, r.handlerActive, joined, r.deadlineKnown, r.deadline, found, r.stackTruncated)
	fmt.Fprintf(&out, "expiry_client_phase=%s expiry_handler_phase=%s expiry_handler_active=%t\n", r.expiryClientPhase, r.expiryHandlerPhase, r.expiryHandlerActive)
	fmt.Fprintf(&out, "ctx_error=%s ctx_observed_offset_us=%d transport_error=%s read_error=%s close_error=%s\n", r.expiryError, r.expiryOffset, r.transportError, r.readError, r.closeError)
	fmt.Fprintf(&out, "records=%d records_dropped=%d prior_delivered_bytes=%d prior_dropped_bytes=%d prior_output_error=%s delivery=current_attempt_unknown\n", r.count, r.dropped, d.delivered, d.droppedBytes, d.outputError)
	for _, e := range r.events[:r.count] {
		fmt.Fprintf(&out, "trace=%s offset_us=%d stage=%s error=%s value=%d\n", r.id, e.offset, e.stage, e.err, e.value)
	}
	out.WriteString(r.stack)
	r.mu.Unlock()
	payload := out.String()
	truncated := len(payload) > neverShipControlOutput-64
	if truncated {
		payload = payload[:neverShipControlOutput-64]
	}
	payload += fmt.Sprintf("output_truncated=%t\n", truncated)
	n, err := io.WriteString(d.out, payload)
	if n < 0 {
		n = 0
	}
	if n > len(payload) {
		n = len(payload)
	}
	d.delivered += n
	d.droppedBytes += len(payload) - n
	d.outputError = neverShipControlError(err)
	if n != len(payload) && err == nil {
		d.outputError = "short_write"
	}
}
func (d *neverShipControlTrace) cleanup() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	traces, dropped := d.traces, d.dropped
	d.mu.Unlock()
	for _, r := range traces {
		if r != nil {
			r.finish()
			d.emit(r, "cleanup")
		}
	}
	d.outputMu.Lock()
	defer d.outputMu.Unlock()
	// Final delivery status describes previous attempts; this write's delivery is
	// intrinsically unknowable to a reader when the stderr sink itself fails.
	_, _ = fmt.Fprintf(d.out, "NEVER-SHIP fixture=%s requests_dropped=%d delivered_bytes=%d dropped_bytes=%d output_error=%s final_delivery=unknown handler_drain=unproven\n", d.fixture, dropped, d.delivered, d.droppedBytes, d.outputError)
}
