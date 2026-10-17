package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

// NEVER-SHIP: observer qualifiers; these do not qualify a production cleanup fix.
func TestConsoleDecisionDefaultDisabled(t *testing.T) {
	if os.Getenv(consoleDecisionEnv) != "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConsoleDecisionDefaultDisabled$", "-test.v", "-test.timeout=30s")
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, consoleDecisionEnv) {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatal("actual default-disabled subprocess", err)
		}
		return
	}
	p := &ownedProcess{}
	beginConsoleDecision(p)
	if p.decision.Load() != nil {
		t.Fatal("default enabled observation")
	}
	if n := testing.AllocsPerRun(100, func() {
		beginConsoleDecision(p)
		consoleDecisionEvent(p, "disabled", nil)
		consoleDecisionRootWait(p, 0, nil)
		consoleDecisionCode(p, nil)
		consoleDecisionAccounting(p, windowsJobAccounting{}, nil)
		consoleDecisionInjection(p, 0, 0, nil)
		consoleDecisionFinal(p, context.Background(), false, 0)
		consoleDecisionOutcome(p, nil, nil)
		consoleDecisionForced(p, "disabled")
		consoleDecisionReturn(p, context.Background(), 0, nil)
		consoleDecisionCanceled(p, context.Background())
		if err := consoleDecisionResult(p, "disabled", io.ErrClosedPipe); err != io.ErrClosedPipe {
			t.Fatal("disabled observer changed error")
		}
	}); n != 0 {
		t.Fatalf("disabled allocations: %v", n)
	}
}

func TestConsoleDecisionConcurrentPublication(t *testing.T) {
	isolated := false
	for _, arg := range os.Args[1:] {
		isolated = isolated || arg == "-test.run=^TestConsoleDecisionConcurrentPublication$"
	}
	// A full suite may already have exhausted the process-wide trace budget.
	if os.Getenv(consoleDecisionEnv) != "1" || !isolated {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConsoleDecisionConcurrentPublication$", "-test.v", "-test.timeout=30s")
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, consoleDecisionEnv) {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, consoleDecisionEnv+"=1")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatal("actual enabled concurrent observer subprocess", err)
		}
		return
	}
	f, err := os.CreateTemp(t.TempDir(), "publication-stderr")
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = stderr
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	p, _ := task43AdmissionJob(t)
	if err := windows.DuplicateHandle(windows.CurrentProcess(), p.anchor.Process, windows.CurrentProcess(), &p.root, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	root, job := p.root, p.job
	gate := make(chan struct{})
	read := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(3)
	p.mu.Lock()
	go func() {
		defer workers.Done()
		<-gate
		beginConsoleDecision(p)
	}()
	go func() {
		defer workers.Done()
		<-gate
		if err := p.stop(); err != nil {
			t.Error("original owned stop", err)
		}
	}()
	go func() {
		defer workers.Done()
		<-gate
		for i := 0; i < 32; i++ {
			consoleDecisionEvent(p, "concurrent-read", nil)
		}
		close(read)
	}()
	close(gate)
	<-read
	p.mu.Unlock()
	workers.Wait()
	consoleDecisionEvent(p, "after-publication", nil)
	if !p.forced || !p.signaled || p.retired || p.err != nil || p.root != root || p.job != job {
		t.Fatal("observer changed original stop state")
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	var traceID uint64
	var begin, after bool
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var frame consoleDecisionFrame
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("CONSOLE-DECISION ")), &frame); err != nil {
			t.Fatal(err)
		}
		if traceID == 0 {
			traceID = frame.TraceID
			if frame.Event != "begin" || frame.Sequence != 1 {
				t.Fatal("observer published before its begin frame")
			}
		}
		if traceID == 0 || frame.TraceID != traceID {
			t.Fatal("publication lost correlation")
		}
		begin = begin || frame.Event == "begin"
		after = after || frame.Event == "after-publication"
	}
	if !begin || !after {
		t.Fatal("enabled observer did not publish real frames")
	}
}

func TestConsoleDecisionUnknownAndErrorSeparation(t *testing.T) {
	r := consoleDecisionFrame{Event: "wait-outcome", Cleanup: consoleDecisionError(context.Canceled), Wait: consoleDecisionError(io.ErrClosedPipe)}
	data, err := formatConsoleDecision(r)
	if err != nil {
		t.Fatal(err)
	}
	var got consoleDecisionFrame
	if err := json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(data, []byte("CONSOLE-DECISION "))), &got); err != nil {
		t.Fatal(err)
	}
	if got.KernelCode != nil || got.WaitStatus != nil || got.AccountingActive != nil || got.AccountingTotal != nil || got.Forced != nil {
		t.Fatalf("unknown became zero: %s", data)
	}
	if got.Cleanup == got.Wait || got.Cleanup == "" || got.Wait == "" {
		t.Fatal("errors merged", got)
	}
}

func TestConsoleDecisionBoundedCorrelation(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := &consoleDecisionTrace{id: 42, output: f}
	p := &ownedProcess{}
	p.decision.Store(d)
	for i := 0; i < consoleDecisionFrameLimit+10; i++ {
		consoleDecisionEvent(p, "probe", errors.New(strings.Repeat("x", 8192)))
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != consoleDecisionFrameLimit {
		t.Fatal("unbounded frame count", len(lines))
	}
	for i, line := range lines {
		if len(line)+1 > consoleDecisionOutputLimit {
			t.Fatal("frame too large")
		}
		var r consoleDecisionFrame
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("CONSOLE-DECISION ")), &r); err != nil {
			t.Fatal(err)
		}
		if r.TraceID != 42 || r.Sequence != uint32(i+1) || !strings.Contains(r.Limits, "NEVER-SHIP") {
			t.Fatal("correlation lost", r)
		}
	}
	if !bytes.Contains(lines[len(lines)-1], []byte("record-limit")) {
		t.Fatal("missing truncation evidence")
	}
	if p.err != nil || p.forced || p.exited {
		t.Fatal("observer changed state")
	}
}

func TestConsoleDecisionDeliveryFailure(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	d := &consoleDecisionTrace{id: 43, output: f}
	p := &ownedProcess{}
	p.decision.Store(d)
	consoleDecisionEvent(p, "closed", nil)
	if !d.deliveryFailed || p.err != nil || p.forced {
		t.Fatal("delivery failure affected outcome or was lost")
	}
}

func TestConsoleDecisionCapturedInputsAndUnchangedErrors(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "frames")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := &ownedProcess{exited: true, exitCode: 23}
	p.decision.Store(&consoleDecisionTrace{id: 44, output: f})
	original := errors.New("original observer failure")
	if got := consoleDecisionResult(p, "resource-close", original); got != original {
		t.Fatal("original error replaced")
	}
	consoleDecisionRootWait(p, 0, nil)
	consoleDecisionCode(p, nil)
	consoleDecisionOutcome(p, context.Canceled, original)
	consoleDecisionInjection(p, 3, 28, io.ErrShortWrite)
	consoleDecisionForced(p, "retire-active")
	consoleDecisionFinal(p, context.Background(), false, 23)
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	frames := map[string]consoleDecisionFrame{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var r consoleDecisionFrame
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("CONSOLE-DECISION ")), &r); err != nil {
			t.Fatal(err)
		}
		frames[r.Event] = r
	}
	code := frames["actual-root-code"]
	if code.KernelCode == nil || *code.KernelCode != 23 || code.Exited == nil || !*code.Exited {
		t.Fatal("publication lost", code)
	}
	injection := frames["injection"]
	if injection.Written == nil || *injection.Written != 3 || injection.Expected == nil || *injection.Expected != 28 || injection.Error != io.ErrShortWrite.Error() {
		t.Fatal("injection evidence lost", injection)
	}
	outcome := frames["wait-outcome"]
	if outcome.Cleanup != context.Canceled.Error() || outcome.Wait != original.Error() {
		t.Fatal("outcome evidence merged", outcome)
	}
	final := frames["pre-final-predicate-snapshot"]
	if final.FirstForced != "retire-active" || final.Promotion == nil || *final.Promotion {
		t.Fatal("diagnostic changed predicate", final)
	}
	if p.forced || p.err != nil || p.exitCode != 23 || !p.exited {
		t.Fatal("diagnostic mutated original inputs")
	}
}
