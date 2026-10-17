package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"
)

// NEVER SHIP: private, Windows-only diagnostic metadata, never process authority.
// Records contain no error strings, command arguments, environment or payloads.
const nativeObservationFrames = 256
const nativeObservationPhases = 128

type nativeObservationRecord struct {
	Operation           string `json:"op"`
	Side                string `json:"side"`
	Sequence            uint64 `json:"seq"`
	Nanos               int64  `json:"local_ns"`
	Phase               string `json:"phase"`
	Frame               string `json:"frame,omitempty"`
	ID                  uint64 `json:"id,omitempty"`
	Causes              uint32 `json:"causes,omitempty"`
	Value               int64  `json:"value,omitempty"`
	Dropped             uint64 `json:"dropped,omitempty"`
	Bytes               uint64 `json:"bytes,omitempty"`
	OutputFailed        bool   `json:"output_failed,omitempty"`
	MissingBytes        uint64 `json:"missing_output_bytes,omitempty"`
	OmittedBytesUnknown bool   `json:"omitted_bytes_unknown,omitempty"`
	Truncated           bool   `json:"truncated,omitempty"`
}

// NativeExchangeDiagnostic is an internal diagnostic seam shared with Service.
// A nil pointer does no observer work. IO is synchronous, bounded in calls and
// bytes, and may perturb timing; no emitter or context watcher is introduced.
type NativeExchangeDiagnostic struct {
	mu                       sync.Mutex
	writer                   io.Writer
	operation, side          string
	start                    time.Time
	sequence, dropped, bytes uint64
	missingBytes             uint64
	frames, phases           int
	failed, closed           bool
}

func NewNativeExchangeDiagnostic(writer io.Writer, operation, side string) *NativeExchangeDiagnostic {
	return &NativeExchangeDiagnostic{writer: writer, operation: operation, side: side, start: time.Now()}
}

type nativeExchangeDiagnosticKey struct{}
type nativeExchangeDiagnosticContext struct {
	observer *NativeExchangeDiagnostic
	metadata string
}

func WithNativeExchangeDiagnostic(ctx context.Context, observer *NativeExchangeDiagnostic, metadata string) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, nativeExchangeDiagnosticKey{}, nativeExchangeDiagnosticContext{observer, metadata})
}

func nativeExchangeDiagnosticFrom(ctx context.Context) nativeExchangeDiagnosticContext {
	d, _ := ctx.Value(nativeExchangeDiagnosticKey{}).(nativeExchangeDiagnosticContext)
	return d
}

// Bit flags retain independent EOF, cancellation, deadline and kernel failures.
// A record is an observation, not a replacement for the original error tree.
func nativeObservationCauses(err error) uint32 {
	var bits uint32
	for i, cause := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, context.Canceled, context.DeadlineExceeded, syscall.Errno(109), syscall.Errno(232)} {
		if errors.Is(err, cause) {
			bits |= 1 << i
		}
	}
	if err != nil {
		bits |= 1 << 31
	}
	return bits
}

func (d *NativeExchangeDiagnostic) Observe(phase, frame string, id uint64, err error, value int64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.sequence++
	framed := strings.HasPrefix(phase, "read.") || strings.HasPrefix(phase, "write.")
	// Preserve the failing boundary even after successful frame chatter fills
	// its quota. The separate lifecycle quota remains a hard output bound.
	if framed && d.frames >= nativeObservationFrames && err != nil {
		framed = false
	}
	if d.failed || len(phase) > 64 || len(frame) > 16 || (framed && d.frames >= nativeObservationFrames) || (!framed && d.phases >= nativeObservationPhases) {
		d.dropped++
		return
	}
	if framed {
		d.frames++
	} else {
		d.phases++
	}
	d.write(nativeObservationRecord{Operation: d.operation, Side: d.side, Sequence: d.sequence, Nanos: time.Since(d.start).Nanoseconds(), Phase: phase, Frame: frame, ID: id, Causes: nativeObservationCauses(err), Value: value})
}

func (d *NativeExchangeDiagnostic) Finish() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	d.sequence++
	d.write(nativeObservationRecord{Operation: d.operation, Side: d.side, Sequence: d.sequence, Nanos: time.Since(d.start).Nanoseconds(), Phase: "capture.finish", Dropped: d.dropped, Bytes: d.bytes, OutputFailed: d.failed, MissingBytes: d.missingBytes, OmittedBytesUnknown: d.dropped != 0, Truncated: d.dropped != 0 || d.failed})
}

func (d *NativeExchangeDiagnostic) write(record nativeObservationRecord) {
	data, err := json.Marshal(record)
	if err != nil || len(data) > 512 {
		d.failed = true
		return
	}
	data = append(data, '\n')
	n, err := d.writer.Write(data)
	if n > 0 {
		d.bytes += uint64(n)
	}
	if err != nil || n != len(data) {
		if n >= 0 && n < len(data) {
			d.missingBytes += uint64(len(data) - n)
		}
		d.failed = true
	}
}
