package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
)

// Frozen b4e47f8 stream control: preserve its actual byte/error/allocation path.
type originalNativeCoreStream struct {
	reader io.Reader
	writer io.Writer
	mu     sync.Mutex
}

func (s *originalNativeCoreStream) send(frame nativeCoreFrame) error {
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

func (s *originalNativeCoreStream) receive() (nativeCoreFrame, error) {
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

type coreObservationRecord struct {
	Phase  string `json:"phase"`
	Frame  string `json:"frame"`
	Causes uint32 `json:"causes"`
}

func coreObservationRecords(t *testing.T, b *bytes.Buffer) []coreObservationRecord {
	t.Helper()
	var records []coreObservationRecord
	d := json.NewDecoder(b)
	for {
		var r coreObservationRecord
		err := d.Decode(&r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}

func TestNativeCoreObservationDisabledProtocolParity(t *testing.T) {
	frame := nativeCoreFrame{Type: "cancel"}
	var original, actual bytes.Buffer
	old := &originalNativeCoreStream{writer: &original}
	current := &nativeCoreStream{writer: &actual}
	if old.send(frame) != current.send(frame) || !bytes.Equal(original.Bytes(), actual.Bytes()) {
		t.Fatal("disabled bytes/errors differ")
	}
	for _, raw := range [][]byte{nil, {1, 0}, {8, 0, 0, 0}, {8, 0, 0, 0, '{'}, original.Bytes()} {
		a, ae := (&originalNativeCoreStream{reader: bytes.NewReader(raw)}).receive()
		b, be := (&nativeCoreStream{reader: bytes.NewReader(raw)}).receive()
		if !reflect.DeepEqual(a, b) || ae != be {
			t.Fatalf("disabled frame/error identity: %#v %#v %v %v", a, b, ae, be)
		}
	}
	before := testing.AllocsPerRun(100, func() { original.Reset(); _ = old.send(frame) })
	after := testing.AllocsPerRun(100, func() { actual.Reset(); _ = current.send(frame) })
	if before != after {
		t.Fatal("disabled send allocations", before, after)
	}
	raw := append([]byte(nil), actual.Bytes()...)
	before = testing.AllocsPerRun(100, func() { _, _ = (&originalNativeCoreStream{reader: bytes.NewReader(raw)}).receive() })
	after = testing.AllocsPerRun(100, func() { _, _ = (&nativeCoreStream{reader: bytes.NewReader(raw)}).receive() })
	if before != after {
		t.Fatal("disabled receive allocations", before, after)
	}
	if !nativeCoreObservationEnabled && newNativeCoreObservation(false) != nil {
		t.Fatal("default observer")
	}
	if !nativeCoreObservationEnabled {
		if n := testing.AllocsPerRun(100, func() {
			if newNativeCoreObservation(false) != nil || newNativeCoreObservation(true) != nil {
				t.Fatal("disabled initialization")
			}
		}); n != 0 {
			t.Fatal("disabled initialization allocations", n)
		}
	} else {
		t.Log("disabled initialization cost requires process-start flag off")
	}
	var disabled *nativeCoreObservation
	ctx := context.Background()
	if n := testing.AllocsPerRun(100, func() {
		if disabled.context(ctx) != ctx || disabled.observer() != nil {
			t.Fatal("disabled observer/context identity")
		}
		disabled.close()
	}); n != 0 {
		t.Fatal("disabled service seam allocations", n)
	}
}

func TestNativeCoreObservationRealPipeBoundaries(t *testing.T) {
	for _, phase := range []string{"read.header.end", "read.payload.end", "write.header.end", "write.payload.end"} {
		t.Run(phase, func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			var output bytes.Buffer
			observer := session.NewNativeExchangeDiagnostic(&output, "00000000000000000000000000000001", "parent")
			stream := &nativeCoreStream{reader: r, writer: w, observer: observer}
			joined := make(chan struct{})
			var err error
			switch phase {
			case "read.header.end":
				go func() { defer close(joined); _ = w.Close() }()
				_, err = stream.receive()
			case "read.payload.end":
				go func() { defer close(joined); _ = binary.Write(w, binary.LittleEndian, uint32(9)); _ = w.Close() }()
				_, err = stream.receive()
			case "write.header.end":
				_ = r.Close()
				close(joined)
				err = stream.send(nativeCoreFrame{Type: "cancel"})
			case "write.payload.end":
				go func() {
					defer close(joined)
					var size uint32
					_ = binary.Read(r, binary.LittleEndian, &size)
					_ = r.Close()
				}()
				err = stream.send(nativeCoreFrame{Type: "cancel"})
			}
			<-joined
			if err != io.EOF && err != io.ErrClosedPipe {
				t.Fatal("original pipe error lost", err)
			}
			records := coreObservationRecords(t, &output)
			last := records[len(records)-1]
			if last.Phase != phase || last.Causes == 0 {
				t.Fatal("wrong observed boundary", last)
			}
		})
	}
}

func TestNativeCoreObservationDoneACKAndIndependentEOF(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			input, writer := io.Pipe()
			var output, trace bytes.Buffer
			observer := session.NewNativeExchangeDiagnostic(&trace, "00000000000000000000000000000001", "helper")
			stream := &nativeCoreStream{reader: input, writer: &output, observer: observer}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rpc := newNativeCoreRPC(ctx, cancel, stream)
			rpc.final.Store(true)
			if err := stream.send(nativeCoreFrame{Type: "done", Done: &nativeCoreDone{Error: nativeCoreEncodeError(context.Canceled)}}); err != nil {
				t.Fatal(err)
			}
			if ack {
				if err := (&nativeCoreStream{writer: writer}).send(nativeCoreFrame{Type: "ack"}); err != nil {
					t.Fatal(err)
				}
			}
			_ = writer.Close()
			<-rpc.finished
			_ = input.Close()
			if ack {
				select {
				case <-rpc.ack:
				default:
					t.Fatal("missing ACK")
				}
				if rpc.err != nil {
					t.Fatal(rpc.err)
				}
			} else if rpc.err != io.EOF {
				t.Fatal("independent EOF lost", rpc.err)
			}
			records := coreObservationRecords(t, &trace)
			found := false
			for _, r := range records {
				if r.Phase == "rpc.ack.accepted" {
					found = true
				}
			}
			if found != ack {
				t.Fatal("ACK capture")
			}
		})
	}
	for _, err := range []error{context.Canceled, errors.Join(context.Canceled, io.EOF), errors.Join(context.Canceled, session.ErrNativeCleanupUnresolved)} {
		before := nativeCoreEncodeError(err)
		var trace bytes.Buffer
		session.NewNativeExchangeDiagnostic(&trace, "00000000000000000000000000000001", "parent").Observe("controlled.error", "", 0, err, 0)
		after := nativeCoreEncodeError(err)
		if !reflect.DeepEqual(before, after) || before.OnlyCanceled != (err == context.Canceled) {
			t.Fatal("diagnostic altered independent cause")
		}
	}
}

func TestNativeCoreObservationBeforeDoneEOF(t *testing.T) {
	input, writer := io.Pipe()
	var output, trace bytes.Buffer
	observer := session.NewNativeExchangeDiagnostic(&trace, "00000000000000000000000000000001", "helper")
	stream := &nativeCoreStream{reader: input, writer: &output, observer: observer}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rpc := newNativeCoreRPC(ctx, cancel, stream)
	_ = writer.Close()
	<-rpc.finished
	_ = input.Close()
	if rpc.final.Load() || rpc.err != io.EOF || ctx.Err() != context.Canceled || output.Len() != 0 {
		t.Fatal("before-Done EOF/control", rpc.err, ctx.Err(), output.Len())
	}
	found := false
	for _, r := range coreObservationRecords(t, &trace) {
		if r.Phase == "rpc.reader.error" && r.Causes != 0 {
			found = true
		}
		if r.Frame == "done" || r.Frame == "ack" {
			t.Fatal("invented completion", r)
		}
	}
	if !found {
		t.Fatal("missing before-Done EOF")
	}
}
