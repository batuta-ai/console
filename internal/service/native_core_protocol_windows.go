package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
)

const nativeCoreFrameLimit = 4 << 20

type nativeCoreHello struct {
	Protocol     int    `json:"protocol"`
	CorePath     string `json:"core_path"`
	CoreVersion  string `json:"core_version"`
	CoreSum      string `json:"core_sum"`
	CoreReplaced bool   `json:"core_replaced"`
	PID          uint32 `json:"pid"`
	Created      uint64 `json:"created"`
}

type nativeCoreStart struct {
	Receipt     domain.PlanApproval            `json:"receipt"`
	Delivery    string                         `json:"delivery"`
	Resume      string                         `json:"resume,omitempty"`
	Skills      string                         `json:"skills"`
	Environment []string                       `json:"environment"`
	Accounts    map[string]nativeBridgeAccount `json:"accounts"`
}

type nativeCoreGuard struct {
	Cleanup    bool     `json:"cleanup,omitempty"`
	Executable string   `json:"executable,omitempty"`
	Directory  string   `json:"directory,omitempty"`
	Args       []string `json:"args,omitempty"`
	Readonly   bool     `json:"readonly,omitempty"`
}

type nativeCoreError struct {
	Message   string   `json:"message"`
	Canceled  bool     `json:"canceled,omitempty"`
	Deadline  bool     `json:"deadline,omitempty"`
	Cleanup   bool     `json:"cleanup,omitempty"`
	Uncertain bool     `json:"uncertain,omitempty"`
	Kernel    []uint32 `json:"kernel,omitempty"`
}

func nativeCoreEncodeError(err error) *nativeCoreError {
	if err == nil {
		return nil
	}
	out := &nativeCoreError{Message: err.Error(), Canceled: errors.Is(err, context.Canceled), Deadline: errors.Is(err, context.DeadlineExceeded), Cleanup: errors.Is(err, session.ErrNativeCleanupUnresolved), Uncertain: errors.Is(err, domain.ErrExecutionUncertain)}
	var visit func(error)
	visit = func(cause error) {
		if code, ok := cause.(syscall.Errno); ok {
			for _, prior := range out.Kernel {
				if prior == uint32(code) {
					return
				}
			}
			out.Kernel = append(out.Kernel, uint32(code))
		}
		switch e := cause.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range e.Unwrap() {
				visit(inner)
			}
		case interface{ Unwrap() error }:
			visit(e.Unwrap())
		}
	}
	visit(err)
	return out
}

func (e *nativeCoreError) cause() error {
	if e == nil {
		return nil
	}
	causes := []error{errors.New(e.Message)}
	if e.Canceled {
		causes = append(causes, context.Canceled)
	}
	if e.Deadline {
		causes = append(causes, context.DeadlineExceeded)
	}
	if e.Cleanup {
		causes = append(causes, session.ErrNativeCleanupUnresolved)
	}
	if e.Uncertain {
		causes = append(causes, domain.ErrExecutionUncertain)
	}
	for _, code := range e.Kernel {
		causes = append(causes, syscall.Errno(code))
	}
	return errors.Join(causes...)
}

type nativeCoreDone struct {
	State   string           `json:"state"`
	Error   *nativeCoreError `json:"error,omitempty"`
	Cleanup *nativeCoreError `json:"cleanup,omitempty"`
}

type nativeCoreFrame struct {
	Type  string           `json:"type"`
	ID    uint64           `json:"id,omitempty"`
	Hello *nativeCoreHello `json:"hello,omitempty"`
	Start *nativeCoreStart `json:"start,omitempty"`
	Guard *nativeCoreGuard `json:"guard,omitempty"`
	Done  *nativeCoreDone  `json:"done,omitempty"`
	Error *nativeCoreError `json:"error,omitempty"`
}

func (f nativeCoreFrame) valid() bool {
	if f.Type != "hello" && f.Hello != nil || f.Type != "start" && f.Start != nil || f.Type != "guard" && f.Guard != nil || f.Type != "done" && f.Done != nil || f.Type != "response" && f.Error != nil {
		return false
	}
	switch f.Type {
	case "hello":
		return f.ID == 0 && f.Hello != nil
	case "start":
		return f.ID == 0 && f.Start != nil
	case "ready":
		return f.ID != 0
	case "guard":
		return f.ID != 0 && f.Guard != nil
	case "response":
		return f.ID != 0
	case "done":
		return f.ID == 0 && f.Done != nil
	case "cancel", "ack":
		return f.ID == 0
	default:
		return false
	}
}

type nativeCoreStream struct {
	reader io.Reader
	writer io.Writer
	mu     sync.Mutex
}

func (s *nativeCoreStream) send(frame nativeCoreFrame) error {
	if !frame.valid() {
		return domain.ErrInvalidReference
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > nativeCoreFrameLimit {
		return fmt.Errorf("native Core frame exceeds bound: %w", domain.ErrInvalidReference)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := binary.Write(s.writer, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err = io.Copy(s.writer, bytes.NewReader(data))
	return err
}

func (s *nativeCoreStream) receive() (nativeCoreFrame, error) {
	var size uint32
	if err := binary.Read(s.reader, binary.LittleEndian, &size); err != nil {
		return nativeCoreFrame{}, err
	}
	if size == 0 || size > nativeCoreFrameLimit {
		return nativeCoreFrame{}, domain.ErrInvalidReference
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(s.reader, data); err != nil {
		return nativeCoreFrame{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var frame nativeCoreFrame
	if err := decoder.Decode(&frame); err != nil {
		return frame, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || !frame.valid() {
		return frame, domain.ErrInvalidReference
	}
	return frame, nil
}

// Requests are serialized; cancellation remains readable while a Core callback
// awaits its parent response, including context.WithoutCancel finalization.
type nativeCoreRPC struct {
	stream    *nativeCoreStream
	mu        sync.Mutex
	next      uint64
	responses chan nativeCoreFrame
	finished  chan struct{}
	ack       chan struct{}
	final     atomic.Bool
	inflight  atomic.Uint64
	err       error
	master    context.Context
}

func newNativeCoreRPC(master context.Context, cancel context.CancelFunc, stream *nativeCoreStream) *nativeCoreRPC {
	r := &nativeCoreRPC{stream: stream, master: master, responses: make(chan nativeCoreFrame, 1), finished: make(chan struct{}), ack: make(chan struct{})}
	go func() {
		defer close(r.finished)
		var last uint64
		for {
			frame, err := stream.receive()
			if err != nil {
				r.err = err
				cancel()
				return
			}
			switch frame.Type {
			case "cancel":
				cancel()
			case "response":
				if frame.ID != last+1 || frame.ID != r.inflight.Load() || r.final.Load() {
					r.err = domain.ErrInvalidReference
					cancel()
					return
				}
				last = frame.ID
				select {
				case r.responses <- frame:
				default:
					r.err = domain.ErrInvalidReference
					cancel()
					return
				}
			case "ack":
				if !r.final.Load() {
					r.err = domain.ErrInvalidReference
					cancel()
					return
				}
				close(r.ack)
				return
			default:
				r.err = domain.ErrInvalidReference
				cancel()
				return
			}
		}
	}()
	return r
}

func (r *nativeCoreRPC) call(ctx context.Context, kind string, guard *nativeCoreGuard) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.next++
	r.inflight.Store(r.next)
	defer r.inflight.Store(0)
	if err := r.stream.send(nativeCoreFrame{Type: kind, ID: r.next, Guard: guard}); err != nil {
		return err
	}
	select {
	case response := <-r.responses:
		if response.ID != r.next {
			return domain.ErrInvalidReference
		}
		return errors.Join(response.Error.cause(), ctx.Err())
	case <-r.finished:
		return errors.Join(r.err, io.ErrUnexpectedEOF)
	}
}
