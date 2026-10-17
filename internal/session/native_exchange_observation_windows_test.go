package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func observationRecords(t *testing.T, data []byte) []nativeObservationRecord {
	t.Helper()
	var records []nativeObservationRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var record nativeObservationRecord
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestNativeObservationCaptureIsolationAndBounds(t *testing.T) {
	var a, b bytes.Buffer
	first := NewNativeExchangeDiagnostic(&a, "00000000000000000000000000000001", "parent")
	second := NewNativeExchangeDiagnostic(&b, "00000000000000000000000000000002", "parent")
	first.Observe("read.header.end", "", 0, io.EOF, 0)
	second.Observe("write.payload.end", "cancel", 0, io.ErrClosedPipe, 0)
	for range nativeObservationFrames + 10 {
		first.Observe("read.header.begin", "", 0, nil, 0)
	}
	first.Observe("exchange.joined", "", 0, errors.Join(io.EOF, context.Canceled), -1)
	first.Finish()
	second.Finish()
	ar, br := observationRecords(t, a.Bytes()), observationRecords(t, b.Bytes())
	if len(ar) != nativeObservationFrames+2 || len(br) != 2 {
		t.Fatalf("missing capture or incorrect bound: %d %d", len(ar), len(br))
	}
	if ar[0].Causes != nativeObservationCauses(io.EOF) || br[0].Frame != "cancel" {
		t.Fatal("phase/error lost")
	}
	if ar[len(ar)-1].Dropped != 11 || ar[len(ar)-2].Phase != "exchange.joined" {
		t.Fatal("overflow hid lifecycle or drop count", ar[len(ar)-2:])
	}
	for i, r := range ar {
		if r.Operation == br[0].Operation || (i > 0 && r.Sequence <= ar[i-1].Sequence) {
			t.Fatal("operation/sequence isolation")
		}
	}
	if a.Len() > (nativeObservationFrames+nativeObservationPhases+1)*513 {
		t.Fatal("byte bound")
	}
}

type observationFailWriter struct{ calls int }

func (w *observationFailWriter) Write([]byte) (int, error) { w.calls++; return 0, io.ErrClosedPipe }

func TestNativeObservationOutputFailureAndNilCost(t *testing.T) {
	w := &observationFailWriter{}
	d := NewNativeExchangeDiagnostic(w, "00000000000000000000000000000001", "helper")
	d.Observe("run.begin", "", 0, nil, 0)
	for range 1000 {
		d.Observe("run.end", "", 0, nil, 0)
	}
	d.Finish()
	if !d.failed || w.calls != 2 {
		t.Fatal("output failure not explicit/bounded", d.failed, w.calls)
	}
	var disabled *NativeExchangeDiagnostic
	ctx := context.Background()
	if WithNativeExchangeDiagnostic(ctx, disabled, "") != ctx {
		t.Fatal("disabled context identity")
	}
	if n := testing.AllocsPerRun(100, func() { disabled.Observe("run.begin", "", 0, nil, 0); disabled.Finish() }); n != 0 {
		t.Fatal("disabled allocations", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		admitted := WithNativeExchangeDiagnostic(ctx, disabled, "")
		diagnostic := nativeExchangeDiagnosticFrom(admitted)
		if admitted != ctx || diagnostic.observer != nil || diagnostic.metadata != "" {
			t.Fatal("disabled admission identity")
		}
	}); n != 0 {
		t.Fatal("disabled context seam allocations", n)
	}
}

func TestNativeObservationFailureSurvivesFrameOverflow(t *testing.T) {
	var trace bytes.Buffer
	observer := NewNativeExchangeDiagnostic(&trace, "00000000000000000000000000000001", "parent")
	for range nativeObservationFrames + 10 {
		observer.Observe("read.header.begin", "", 0, nil, 0)
	}
	observer.Observe("read.payload.end", "", 0, io.EOF, 0)
	observer.Finish()
	records := observationRecords(t, trace.Bytes())
	last := records[len(records)-2]
	if last.Phase != "read.payload.end" || last.Causes != nativeObservationCauses(io.EOF) {
		t.Fatal("frame overflow hid the failure boundary", last)
	}
}

type observationRecoverWriter struct {
	bytes.Buffer
	first bool
}

func (w *observationRecoverWriter) Write(data []byte) (int, error) {
	if !w.first {
		w.first = true
		return 0, io.ErrClosedPipe
	}
	return w.Buffer.Write(data)
}
func TestNativeObservationFailureSummaryAndConcurrentSequence(t *testing.T) {
	w := &observationRecoverWriter{}
	d := NewNativeExchangeDiagnostic(w, "00000000000000000000000000000001", "helper")
	d.Observe("run.begin", "", 0, nil, 0)
	d.Finish()
	records := observationRecords(t, w.Bytes())
	if len(records) != 1 || !records[0].OutputFailed || !records[0].Truncated || records[0].MissingBytes == 0 {
		t.Fatal("output failure summary", records)
	}
	var trace bytes.Buffer
	d = NewNativeExchangeDiagnostic(&trace, "00000000000000000000000000000001", "parent")
	var joined sync.WaitGroup
	for range 8 {
		joined.Add(1)
		go func() {
			defer joined.Done()
			for range 8 {
				d.Observe("controlled.concurrent", "", 0, nil, 0)
			}
		}()
	}
	joined.Wait()
	d.Finish()
	records = observationRecords(t, trace.Bytes())
	if len(records) != 65 {
		t.Fatal("concurrent records", len(records))
	}
	for i, r := range records {
		if r.Sequence != uint64(i+1) {
			t.Fatal("concurrent sequence", r.Sequence)
		}
	}
}

// Native qualification below is Windows-only; never included in the pure projection.
func TestNativeObservationExchangeGraceAndRetirement(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, mode := range []string{"cooperative", "hold"} {
			t.Run(fmt.Sprintf("enabled=%v/%s", enabled, mode), func(t *testing.T) {
				runner := task43WindowsNativeRunner(t)
				foreign := task43ForeignSibling(t)
				var trace bytes.Buffer
				var observer *NativeExchangeDiagnostic
				if enabled {
					observer = NewNativeExchangeDiagnostic(&trace, "00000000000000000000000000000001", "parent")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx = WithNativeExchangeDiagnostic(ctx, observer, "")
				var owned *ownedProcess
				var witnesses []windows.Handle
				result, err := runner.Exchange(ctx, task43ExchangeCommand(t, mode, "observation-control"), func(callback context.Context, output io.Reader, input io.WriteCloser) error {
					birth, err := task43ExchangeReceiveBirth(output)
					if err != nil {
						return err
					}
					witnesses = append(witnesses, task43ObserveLifetimeChild(t, birth.Root))
					if mode == "hold" {
						witnesses = append(witnesses, task43ObserveLifetimeChild(t, birth.Child))
					}
					runner.Manager.mu.Lock()
					for p := range runner.Manager.probes {
						owned = p
					}
					runner.Manager.mu.Unlock()
					cancel()
					<-callback.Done()
					if mode == "cooperative" {
						if err := task43ExchangeWrite(input, []byte("stop")); err != nil {
							return err
						}
						response, err := task43ExchangeRead(output)
						if err == nil && string(response) != "released" {
							return errors.New("release response missing")
						}
						return err
					}
					_, err = io.Copy(io.Discard, output)
					return err
				})
				observer.Finish()
				if !errors.Is(err, context.Canceled) || result.ExitCode != -1 || owned == nil || !owned.retired || owned.forced != (mode == "hold") {
					t.Fatal("original outcome/lifecycle", result, err, owned)
				}
				for _, h := range witnesses {
					status, err := windows.WaitForSingleObject(h, 0)
					if err != nil || status != windows.WAIT_OBJECT_0 {
						t.Fatal("original birth handle still live", status, err)
					}
				}
				task43SiblingStillRunning(t, foreign)
				runner.Manager.mu.Lock()
				remaining := len(runner.Manager.probes)
				runner.Manager.mu.Unlock()
				if remaining != 0 {
					t.Fatal("exchange admission not joined")
				}
				if !enabled {
					if trace.Len() != 0 {
						t.Fatal("disabled output")
					}
					return
				}
				records := observationRecords(t, trace.Bytes())
				phases := map[string]nativeObservationRecord{}
				for _, r := range records {
					phases[r.Phase] = r
				}
				for _, phase := range []string{"exchange.callback.joined", "exchange.process.joined", "exchange.stderr.joined", "exchange.wait.native", "exchange.wait.exitcode", "exchange.joined", "exchange.return"} {
					if _, ok := phases[phase]; !ok {
						t.Fatal("missing phase", phase)
					}
				}
				_, expired := phases["exchange.grace.expired"]
				if expired != (mode == "hold") {
					t.Fatal("real two-second timer qualification", expired, mode)
				}
				if phases["exchange.wait.lifecycle"].Value&1 == 0 {
					t.Fatal("retirement observation")
				}
				if mode == "cooperative" && phases["exchange.wait.exitcode"].Value != 0 {
					t.Fatal("diagnostic native exit on cancellation")
				}
			})
		}
	}
}

// Frozen b4e47f8 Exchange control; original ownership and joins remain executable.
func (r NativeRunner) originalNativeExchangeForObservation(ctx context.Context, command publication.Command, handler func(context.Context, io.Reader, io.WriteCloser) error) (result publication.CommandResult, runErr error) {
	result.ExitCode = -1
	if r.Manager == nil || handler == nil || !filepath.IsAbs(command.Executable) || r.Graceful || len(command.Stdin) != 0 || command.Observer != nil || command.StdoutLimit != 0 || command.StderrLimit < 0 || command.StderrLimit > 16<<20 {
		return result, errors.New("invalid native exchange command or manager")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	m := r.Manager
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return result, ErrProbeClosing
	}
	m.active.Add(1)
	m.mu.Unlock()
	defer m.active.Done()
	explicit, privateHome := r.Environment, ""
	if accounts.ExplicitHome(explicit) == "" {
		env, home, err := accounts.SessionEnvironment()
		if err != nil {
			return result, err
		}
		explicit, privateHome = append(append([]string{}, explicit...), env...), home
	}
	defer func() {
		if privateHome != "" {
			if err := os.RemoveAll(privateHome); err != nil {
				result.ExitCode = -1
				runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, err)
				m.CloseAdmission()
				m.recordCleanup(err, err)
			}
		}
	}()
	env, err := NativeEnvironment(explicit, command.Environment)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	pipes, err := newNativeExchangePipes()
	if err != nil {
		return result, err
	}
	defer func() {
		if err := pipes.Close(); err != nil {
			result.ExitCode = -1
			runErr = errors.Join(runErr, ErrNativeCleanupUnresolved, err)
			m.CloseAdmission()
			m.recordCleanup(err, err)
		}
	}()
	limit := command.StderrLimit
	if limit == 0 {
		limit = 64 << 10
	}
	stderr := &nativeOutput{limit: limit}
	cmd := exec.Command(command.Executable, command.Args...)
	cmd.Env, cmd.Dir = env, command.Directory
	// Original standard handles are duplicated into the suspended Job anchor.
	// The parent owns all three drains independently of Cmd.Wait, which can
	// hide copying errors when the child also exits unsuccessfully.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pipes.inputRead.File, pipes.outputWrite.File, pipes.stderrWrite.File
	var stderrWriter io.Writer = stderr
	if command.StderrObserver != nil {
		stderrWriter = io.MultiWriter(stderr, command.StderrObserver)
	}
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		return result, err
	}
	if err := errors.Join(pipes.inputRead.Close(), pipes.outputWrite.Close(), pipes.stderrWrite.Close()); err != nil {
		cleanup := errors.Join(owned.stop(), waitOwned(cmd, owned))
		m.CloseAdmission()
		m.recordCleanup(errors.Join(err, cleanup), cleanup)
		return result, errors.Join(err, cleanup)
	}
	stopContext, requestStop := context.WithCancel(ctx)
	defer requestStop()
	callbackContext, cancelCallback := context.WithCancel(stopContext)
	defer cancelCallback()
	owned.mu.Lock()
	owned.cancel = requestStop
	owned.mu.Unlock()
	m.mu.Lock()
	m.probes[owned] = struct{}{}
	closing := m.closing
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.probes, owned)
		m.mu.Unlock()
	}()
	if closing {
		requestStop()
	}
	callbackDone := make(chan error, 1)
	go func() { callbackDone <- handler(callbackContext, pipes.outputRead, pipes.inputWrite) }()
	stderrDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stderrWriter, pipes.stderrRead)
		stderrDone <- errors.Join(err, pipes.stderrRead.Close())
	}()
	processDone := make(chan error, 1)
	go func() {
		ownership, wait := waitOwnedOutcome(cmd, owned)
		cleanup := errors.Join(ownership, wait)
		if cleanup != nil {
			cleanup = errors.Join(ErrNativeCleanupUnresolved, cleanup)
			m.CloseAdmission()
			m.recordCleanup(cleanup, cleanup)
		}
		processDone <- cleanup
	}()
	var callbackErr, processErr, stderrErr, stopErr, protocolErr error
	stopOwned := func() {
		if err := owned.stop(); err != nil {
			cleanup := errors.Join(ErrNativeCleanupUnresolved, err)
			stopErr = errors.Join(stopErr, cleanup)
			m.CloseAdmission()
			m.recordCleanup(cleanup, cleanup)
		}
	}
	stopRequested := stopContext.Done()
	var grace *time.Timer
	var expired <-chan time.Time
	startGrace := func() {
		if grace == nil {
			grace = time.NewTimer(2 * time.Second)
			expired = grace.C
		}
	}
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()
	for callbackDone != nil || processDone != nil || stderrDone != nil {
		select {
		case callbackErr = <-callbackDone:
			callbackDone = nil
			callbackErr = errors.Join(callbackErr, pipes.inputWrite.Close())
			if callbackErr != nil {
				stopOwned()
			} else if processDone != nil {
				startGrace()
			}
		case processErr = <-processDone:
			processDone = nil
			expired = nil
			if grace != nil {
				grace.Stop()
			}
			cancelCallback()
		case stderrErr = <-stderrDone:
			stderrDone = nil
			if stderrErr != nil {
				cancelCallback()
				stopOwned()
			}
		case <-stopRequested:
			stopRequested = nil
			startGrace()
		case <-expired:
			expired = nil
			stopOwned()
			if stopContext.Err() == nil {
				protocolErr = errors.Join(errors.New("native exchange did not finish after protocol closure"), context.DeadlineExceeded)
			}
		}
	}
	result.Stderr, result.StderrTruncated = append([]byte(nil), stderr.data...), stderr.truncated
	runErr = errors.Join(callbackErr, processErr, stderrErr, stopErr, protocolErr, stopContext.Err())
	if runErr == nil && cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, runErr
}

func TestNativeObservationDisabledOriginalExchangeParity(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	command := task43ExchangeCommand(t, "roundtrip", "disabled-parity")
	var transcripts [2][]byte
	var results [2]publication.CommandResult
	for i, exchange := range []func(context.Context, publication.Command, func(context.Context, io.Reader, io.WriteCloser) error) (publication.CommandResult, error){runner.originalNativeExchangeForObservation, runner.Exchange} {
		var err error
		results[i], err = exchange(context.Background(), command, func(_ context.Context, output io.Reader, input io.WriteCloser) error {
			if _, err := task43ExchangeReceiveBirth(output); err != nil {
				return err
			}
			for range 3 {
				if err := task43ExchangeWrite(input, []byte("controlled-protocol")); err != nil {
					return err
				}
				data, err := task43ExchangeRead(output)
				if err != nil {
					return err
				}
				transcripts[i] = append(transcripts[i], data...)
			}
			return nil
		})
		if err != nil {
			t.Fatal("exchange control", i, err)
		}
	}
	if !reflect.DeepEqual(results[0], results[1]) || !bytes.Equal(transcripts[0], transcripts[1]) {
		t.Fatal("disabled original exchange outcome/bytes differ", results)
	}
	allocationCallback := func(_ context.Context, output io.Reader, input io.WriteCloser) error {
		if _, err := task43ExchangeReceiveBirth(output); err != nil {
			return err
		}
		for range 3 {
			if err := task43ExchangeWrite(input, []byte("allocation-control")); err != nil {
				return err
			}
			if _, err := task43ExchangeRead(output); err != nil {
				return err
			}
		}
		return nil
	}
	originalAllocs := testing.AllocsPerRun(3, func() {
		result, err := runner.originalNativeExchangeForObservation(context.Background(), command, allocationCallback)
		if err != nil || result.ExitCode != 0 {
			t.Fatal("original allocation control", result, err)
		}
	})
	currentAllocs := testing.AllocsPerRun(3, func() {
		result, err := runner.Exchange(context.Background(), command, allocationCallback)
		if err != nil || result.ExitCode != 0 {
			t.Fatal("current allocation control", result, err)
		}
	})
	// Process startup and concurrent runtime work make this a measurement,
	// not an exact observer-cost qualifier. Outcome assertions above still apply.
	t.Logf("UNQUALIFIED whole Exchange allocations: original=%g current=%g; no full-process cost equivalence claim", originalAllocs, currentAllocs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	callback := func(context.Context, io.Reader, io.WriteCloser) error {
		t.Error("cancelled admission invoked callback")
		return nil
	}
	before := testing.AllocsPerRun(100, func() {
		_, err := runner.originalNativeExchangeForObservation(ctx, command, callback)
		if err != context.Canceled {
			t.Fatal(err)
		}
	})
	after := testing.AllocsPerRun(100, func() {
		_, err := runner.Exchange(ctx, command, callback)
		if err != context.Canceled {
			t.Fatal(err)
		}
	})
	if before != 0 || after != 0 {
		t.Fatal("disabled exchange admission allocations", before, after)
	}
}
