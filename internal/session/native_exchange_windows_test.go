package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

// NativeExchangeTestObserver retains the original owner, but only duplicates
// query/synchronize rights. It cannot request cancellation or terminate a job.
type NativeExchangeTestObserver struct {
	manager *Manager
	owner   *ownedProcess
	witness windows.Handle
}

type NativeExchangeTestState struct {
	Forced, Signaled, Retired, Registered, Exited bool
	TerminationError                              error
	ExitCode                                      uint32
}

func ObserveNativeExchangeForTest(m *Manager, pid uint32, creation uint64) (*NativeExchangeTestObserver, error) {
	if m == nil || pid == 0 || creation == 0 {
		return nil, errors.New("missing native owner identity")
	}
	m.mu.Lock()
	owners := make([]*ownedProcess, 0, len(m.probes))
	for p := range m.probes {
		owners = append(owners, p)
	}
	m.mu.Unlock()
	var observer *NativeExchangeTestObserver
	for _, p := range owners {
		p.mu.Lock()
		if p.retired || p.root == 0 {
			p.mu.Unlock()
			continue
		}
		observedPID, err := windows.GetProcessId(p.root)
		if err != nil || observedPID != pid {
			p.mu.Unlock()
			if err != nil {
				return nil, err
			}
			continue
		}
		var born, exited, kernel, user windows.Filetime
		err = windows.GetProcessTimes(p.root, &born, &exited, &kernel, &user)
		if err == nil && uint64(born.HighDateTime)<<32|uint64(born.LowDateTime) == creation {
			if observer != nil {
				p.mu.Unlock()
				return nil, errors.Join(errors.New("multiple native owners match birth"), observer.Close())
			}
			observer = &NativeExchangeTestObserver{manager: m, owner: p}
			err = windows.DuplicateHandle(windows.CurrentProcess(), p.root, windows.CurrentProcess(), &observer.witness, windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0)
		}
		p.mu.Unlock()
		if err != nil {
			if observer != nil {
				err = errors.Join(err, observer.Close())
			}
			return nil, err
		}
	}
	if observer == nil {
		return nil, errors.New("no native owner matches birth")
	}
	return observer, nil
}

func (o *NativeExchangeTestObserver) Snapshot() (NativeExchangeTestState, error) {
	p := o.owner
	p.mu.Lock()
	s := NativeExchangeTestState{Forced: p.forced, Signaled: p.signaled, Retired: p.retired, TerminationError: p.err}
	p.mu.Unlock()
	o.manager.mu.Lock()
	_, s.Registered = o.manager.probes[p]
	o.manager.mu.Unlock()
	status, err := windows.WaitForSingleObject(o.witness, 0)
	if err != nil {
		return s, err
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		s.Exited = true
		err = windows.GetExitCodeProcess(o.witness, &s.ExitCode)
	case uint32(windows.WAIT_TIMEOUT):
	default:
		err = fmt.Errorf("unexpected witness wait status %d", status)
	}
	return s, err
}

func (o *NativeExchangeTestObserver) Close() error {
	if o.witness == 0 {
		return nil
	}
	err := windows.CloseHandle(o.witness)
	o.witness = 0
	return err
}

func TestNativeCoreOwnerObservationControls(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			runner := task43WindowsNativeRunner(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mode := "cooperative"
			if forced {
				mode = "hold"
			}
			var observer *NativeExchangeTestObserver
			result, err := runner.Exchange(ctx, task43ExchangeCommand(t, mode, "observer-control"), func(_ context.Context, output io.Reader, input io.WriteCloser) error {
				birth, err := task43ExchangeReceiveBirth(output)
				if err != nil {
					return err
				}
				var pid uint32
				var creation uint64
				if _, err := fmt.Sscanf(birth.Root, "LIFETIME-CHILD:%d:%d", &pid, &creation); err != nil {
					return err
				}
				if wrong, err := ObserveNativeExchangeForTest(runner.Manager, pid, creation+1); err == nil {
					return errors.Join(errors.New("observer accepted wrong creation"), wrong.Close())
				}
				observer, err = ObserveNativeExchangeForTest(runner.Manager, pid, creation)
				if err != nil {
					return err
				}
				before, err := observer.Snapshot()
				if err != nil || before.Forced || before.Retired || before.Exited || !before.Registered {
					return errors.Join(fmt.Errorf("invalid live owner: %+v", before), err)
				}
				if forced {
					cancel()
					_, err := task43ExchangeRead(output)
					return err
				}
				if err := task43ExchangeWrite(input, []byte("stop")); err != nil {
					return err
				}
				reply, err := task43ExchangeRead(output)
				if err != nil || string(reply) != "released" {
					return errors.Join(errors.New("missing cooperative reply"), err)
				}
				return nil
			})
			if observer == nil {
				t.Fatal("no original owner retained", result, err)
			}
			defer func() {
				if err := observer.Close(); err != nil {
					t.Error(err)
				}
			}()
			state, observeErr := observer.Snapshot()
			t.Logf("joined owner forced=%t state=%+v exchange=%v", forced, state, err)
			if observeErr != nil || state.TerminationError != nil || !state.Retired || !state.Signaled || !state.Exited || state.Registered || state.Forced != forced {
				t.Fatal("original owner observation failed", state, observeErr)
			}
			if forced {
				if !errors.Is(err, context.Canceled) || result.ExitCode != -1 {
					t.Fatal(result, err)
				}
			} else if err != nil || result.ExitCode != 0 || state.ExitCode != 0 {
				t.Fatal(result, state, err)
			}
		})
	}
}

func task43ExchangeWrite(w io.Writer, data []byte) error {
	if err := binary.Write(w, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err := io.Copy(w, bytes.NewReader(data))
	return err
}

func task43ExchangeRead(r io.Reader) ([]byte, error) {
	var size uint32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return nil, err
	}
	if size > 256<<10 {
		return nil, errors.New("fixture frame exceeds bound")
	}
	data := make([]byte, size)
	_, err := io.ReadFull(r, data)
	return data, err
}

type task43ExchangeBirth struct {
	Root, Child, Home, Secret, Cwd, Arg string
	Pipes                               bool
}

func TestTask43WindowsNativeExchangeFixture(t *testing.T) {
	if len(os.Args) < 6 || os.Args[3] != "task43-native-exchange" {
		return
	}
	mode := os.Args[4]
	if mode == "launch-marker" {
		if err := os.WriteFile(os.Args[5], []byte("launched"), 0600); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	inputType, inputErr := windows.GetFileType(windows.Handle(os.Stdin.Fd()))
	outputType, outputErr := windows.GetFileType(windows.Handle(os.Stdout.Fd()))
	birth := task43ExchangeBirth{Root: fmt.Sprintf("LIFETIME-CHILD:%d:%d", os.Getpid(), uint64(created.HighDateTime)<<32|uint64(created.LowDateTime)), Home: os.Getenv("HOME"), Secret: os.Getenv("OPENAI_API_KEY"), Cwd: cwd, Arg: os.Args[5], Pipes: inputErr == nil && outputErr == nil && inputType == windows.FILE_TYPE_PIPE && outputType == windows.FILE_TYPE_PIPE}
	if mode == "hold" || mode == "natural" {
		argv := task43ProbeArgv(t, "hold")
		child := exec.Command(argv[0], argv[1:]...)
		child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		var observed error
		if err := child.Process.WithHandle(func(h uintptr) {
			observed = windows.GetProcessTimes(windows.Handle(h), &created, &exited, &kernel, &user)
		}); err != nil || observed != nil {
			t.Fatal(err, observed)
		}
		birth.Child = fmt.Sprintf("LIFETIME-CHILD:%d:%d", child.Process.Pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	}
	data, err := json.Marshal(birth)
	if err != nil {
		t.Fatal(err)
	}
	if err := task43ExchangeWrite(os.Stdout, data); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "hold":
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(event)
		if status, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal(status, err)
		}
	case "natural", "cooperative":
		request, err := task43ExchangeRead(os.Stdin)
		if err != nil || string(request) != "stop" {
			t.Fatal("missing stop protocol", string(request), err)
		}
		if mode == "cooperative" {
			if err := task43ExchangeWrite(os.Stdout, []byte("released")); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(0)
	case "roundtrip", "large":
		stderr := "native-exchange-stderr"
		if mode == "large" {
			stderr = strings.Repeat("Z", 64<<10)
		}
		fmt.Fprint(os.Stderr, stderr)
		for i := range 3 {
			request, err := task43ExchangeRead(os.Stdin)
			if err != nil {
				t.Fatal(err)
			}
			if err := task43ExchangeWrite(os.Stdout, append([]byte{byte(i)}, request...)); err != nil {
				t.Fatal(err)
			}
		}
		if tail, err := io.ReadAll(os.Stdin); err != nil || len(tail) != 0 {
			t.Fatal("input did not end cleanly", tail, err)
		}
		if mode == "large" {
			os.Exit(44)
		}
		os.Exit(0)
	default:
		t.Fatal("unknown exchange mode", mode)
	}
}

func task43ExchangeCommand(t *testing.T, mode, arg string) publication.Command {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return publication.Command{Executable: exe, Args: []string{"-test.run=^TestTask43WindowsNativeExchangeFixture$", "--", "task43-native-exchange", mode, arg}}
}

func task43ExchangeReceiveBirth(r io.Reader) (task43ExchangeBirth, error) {
	data, err := task43ExchangeRead(r)
	var birth task43ExchangeBirth
	if err == nil {
		err = json.Unmarshal(data, &birth)
	}
	return birth, err
}

func TestTask43WindowsNativeExchangeRoundTrip(t *testing.T) {
	for _, mode := range []string{"roundtrip", "large"} {
		t.Run(mode, func(t *testing.T) {
			runner := task43WindowsNativeRunner(t)
			t.Setenv("OPENAI_API_KEY", "synthetic-must-not-cross")
			literal := "literal & % ! ç \" \\ UNC\\尾\n$(no-execution)"
			command := task43ExchangeCommand(t, mode, literal)
			command.Directory = t.TempDir()
			command.Environment = []string{"home=foreign", "OPENAI_API_KEY=synthetic-must-not-cross"}
			var observer bytes.Buffer
			command.StderrLimit, command.StderrObserver = 37, &observer
			var birth task43ExchangeBirth
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := runner.Exchange(ctx, command, func(_ context.Context, output io.Reader, input io.WriteCloser) error {
				var err error
				birth, err = task43ExchangeReceiveBirth(output)
				if err != nil {
					return err
				}
				for i, request := range [][]byte{{0, '\r', '\n', 0x1b, 0x1a}, []byte("消息 & \"\n"), bytes.Repeat([]byte("Q\x00"), 32768)} {
					if err := task43ExchangeWrite(input, request); err != nil {
						return err
					}
					reply, err := task43ExchangeRead(output)
					if err != nil || !bytes.Equal(reply, append([]byte{byte(i)}, request...)) {
						return errors.Join(errors.New("dependent binary reply changed"), err)
					}
				}
				if err := input.Close(); err != nil {
					return err
				}
				tail, err := io.ReadAll(output)
				if err != nil || len(tail) != 0 {
					return errors.Join(errors.New("output did not drain cleanly"), err)
				}
				return nil
			})
			wantCode, wantStderr := 0, "native-exchange-stderr"
			if mode == "large" {
				wantCode, wantStderr = 44, strings.Repeat("Z", 64<<10)
			}
			bounded := wantStderr
			if len(bounded) > 37 {
				bounded = bounded[:37]
			}
			if err != nil || result.ExitCode != wantCode || len(result.Stdout) != 0 || result.StdoutTruncated || string(result.Stderr) != bounded || result.StderrTruncated != (len(wantStderr) > 37) || observer.String() != wantStderr || birth.Home != accounts.ExplicitHome(runner.Environment) || birth.Secret != "" || birth.Arg != literal || !strings.EqualFold(birth.Cwd, command.Directory) || !birth.Pipes {
				t.Fatal("exchange changed actual pipe/environment/argv/cwd/stream/exit contract", birth, result, observer.Len(), err)
			}
		})
	}
}

func TestTask43WindowsNativeExchangeOwnedCancellation(t *testing.T) {
	for _, action := range []string{"cancel", "shutdown", "handler-error", "natural"} {
		t.Run(action, func(t *testing.T) {
			runner := task43WindowsNativeRunner(t)
			foreign := task43ForeignSibling(t)
			mode := "hold"
			if action == "natural" {
				mode = "natural"
			}
			command := task43ExchangeCommand(t, mode, "owned")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline, end := context.WithTimeout(context.Background(), 6*time.Second)
			defer end()
			births := make(chan task43ExchangeBirth, 1)
			proceed, callbackDone := make(chan struct{}), make(chan struct{})
			cause := errors.New("actual parent guard rejected helper request")
			var input io.WriteCloser
			var output io.Reader
			type outcome struct {
				result publication.CommandResult
				err    error
			}
			completed := make(chan outcome, 1)
			go func() {
				result, err := runner.Exchange(ctx, command, func(_ context.Context, reader io.Reader, writer io.WriteCloser) error {
					defer close(callbackDone)
					input, output = writer, reader
					birth, err := task43ExchangeReceiveBirth(reader)
					if err != nil {
						return err
					}
					births <- birth
					<-proceed
					if action == "handler-error" {
						return cause
					}
					if action == "natural" {
						if err := task43ExchangeWrite(writer, []byte("stop")); err != nil {
							return err
						}
					}
					_, err = task43ExchangeRead(reader)
					if action == "natural" && errors.Is(err, io.EOF) {
						return nil
					}
					return err
				})
				completed <- outcome{result, err}
			}()
			var birth task43ExchangeBirth
			select {
			case birth = <-births:
			case early := <-completed:
				t.Fatal("exchange exited before actual birth receipt", early)
			case <-deadline.Done():
				t.Fatal("exchange did not publish births", deadline.Err())
			}
			root := task43ObserveLifetimeChild(t, birth.Root)
			child := task43ObserveLifetimeChild(t, birth.Child)
			close(proceed)
			if action == "cancel" {
				cancel()
			} else if action == "shutdown" {
				if err := runner.Manager.Shutdown(deadline); err != nil {
					t.Fatal("shutdown failed to join exchange", err)
				}
			}
			select {
			case ended := <-completed:
				if action == "natural" {
					if ended.err != nil || ended.result.ExitCode != 0 {
						t.Fatal("natural root lost its real result", ended)
					}
				} else if ended.result.ExitCode != -1 || (action == "handler-error" && !errors.Is(ended.err, cause)) || (action != "handler-error" && !errors.Is(ended.err, context.Canceled)) {
					t.Fatal("exchange lost cancellation or actual handler cause", ended)
				}
			case <-deadline.Done():
				t.Fatal("exchange did not join actual tree and handler", deadline.Err())
			}
			select {
			case <-callbackDone:
			default:
				t.Fatal("exchange returned before callback ended")
			}
			for _, handle := range []windows.Handle{root, child} {
				if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("owned process object survived exchange completion", status, err)
				}
			}
			if _, err := input.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
				t.Fatal("input pipe remains writable", err)
			}
			if _, err := output.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
				t.Fatal("output pipe remains readable", err)
			}
			task43SiblingStillRunning(t, foreign)
			runner.Manager.mu.Lock()
			remaining := len(runner.Manager.probes)
			runner.Manager.mu.Unlock()
			if remaining != 0 {
				t.Fatal("exchange retained process admission", remaining)
			}
		})
	}
}

func TestTask43WindowsNativeExchangeCooperativeCancellation(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var owned *ownedProcess
	released := false
	result, err := runner.Exchange(ctx, task43ExchangeCommand(t, "cooperative", "guard"), func(callback context.Context, output io.Reader, input io.WriteCloser) error {
		if _, err := task43ExchangeReceiveBirth(output); err != nil {
			return err
		}
		runner.Manager.mu.Lock()
		for p := range runner.Manager.probes {
			owned = p
		}
		runner.Manager.mu.Unlock()
		cancel()
		<-callback.Done()
		if err := task43ExchangeWrite(input, []byte("stop")); err != nil {
			return err
		}
		message, err := task43ExchangeRead(output)
		if err != nil || string(message) != "released" {
			return errors.Join(errors.New("cooperative release acknowledgement missing"), err)
		}
		released = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || result.ExitCode != -1 || !released || owned == nil || !owned.retired || owned.forced {
		t.Fatal("cancel did not preserve cooperative protocol grace and owned drain", result, err, released, owned)
	}
}

func TestTask43WindowsNativeExchangeRefusalAndStartRecovery(t *testing.T) {
	for _, kind := range []string{"stdin", "observer", "stdout-limit", "graceful", "nil-handler", "closed", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			runner := task43WindowsNativeRunner(t)
			marker := filepath.Join(t.TempDir(), "unexpected-launch")
			command := task43ExchangeCommand(t, "launch-marker", marker)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			handler := func(context.Context, io.Reader, io.WriteCloser) error { called = true; return nil }
			switch kind {
			case "stdin":
				command.Stdin = []byte("finite")
			case "observer":
				command.Observer = io.Discard
			case "stdout-limit":
				command.StdoutLimit = 1
			case "graceful":
				runner.Graceful = true
			case "nil-handler":
				handler = nil
			case "closed":
				runner.Manager.CloseAdmission()
			case "cancelled":
				cancel()
			}
			result, err := runner.Exchange(ctx, command, handler)
			if err == nil || result.ExitCode != -1 || called || (kind == "closed" && !errors.Is(err, ErrProbeClosing)) || (kind == "cancelled" && !errors.Is(err, context.Canceled)) {
				t.Fatal("invalid exchange was admitted", kind, result, err, called)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid exchange launched a process", err)
			}
		})
	}
	runner := task43WindowsNativeRunner(t)
	missing := publication.Command{Executable: filepath.Join(t.TempDir(), "missing.exe")}
	for range 3 {
		result, err := runner.Exchange(context.Background(), missing, func(context.Context, io.Reader, io.WriteCloser) error {
			t.Error("failed start invoked callback")
			return nil
		})
		if err == nil || result.ExitCode != -1 {
			t.Fatal("missing helper unexpectedly launched", result, err)
		}
	}
	result, err := runner.Exchange(context.Background(), task43ExchangeCommand(t, "cooperative", "after-failure"), func(_ context.Context, output io.Reader, input io.WriteCloser) error {
		if _, err := task43ExchangeReceiveBirth(output); err != nil {
			return err
		}
		if err := task43ExchangeWrite(input, []byte("stop")); err != nil {
			return err
		}
		message, err := task43ExchangeRead(output)
		if err != nil || string(message) != "released" {
			return errors.Join(errors.New("recovered exchange did not acknowledge"), err)
		}
		return nil
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatal("failed starts retained admission", result, err)
	}
}

func TestTask4WindowsNativeProtocolDoneRegistration(t *testing.T) {
	// This real-time timer guards setup only; it cannot attest logical expiry.
	setupWatchdog := time.NewTimer(30 * time.Second)
	defer setupWatchdog.Stop()
	synctest.Test(t, func(t *testing.T) {
		runner := task43WindowsNativeRunner(t)
		command := task43ExchangeCommand(t, "cooperative", "protocol-done-registration")
		control, cancel := context.WithCancel(context.Background())
		abort, release := make(chan struct{}), make(chan struct{})
		ready := make(chan NativeExchangeFinalization, 1)
		finished := make(chan struct{})
		var ioJoined <-chan struct{}
		var observer *NativeExchangeTestObserver
		var result publication.CommandResult
		var runErr error
		setupAborted := errors.New("ProtocolDone registration setup aborted")
		go func() {
			defer close(finished)
			result, runErr = runner.ExchangeFinalizing(control, command, func(finalization NativeExchangeFinalization, output io.Reader, input io.WriteCloser) error {
				joined := make(chan struct{})
				ioJoined = joined
				prepared := make(chan error, 1)
				// On setup failure the callback must return before joining I/O:
				// its error makes production terminate the peer and unblock reads.
				go func() {
					defer close(joined)
					prepared <- func() error {
						birth, err := task43ExchangeReceiveBirth(output)
						if err != nil {
							return err
						}
						var pid uint32
						var creation uint64
						if _, err := fmt.Sscanf(birth.Root, "LIFETIME-CHILD:%d:%d", &pid, &creation); err != nil {
							return err
						}
						observer, err = ObserveNativeExchangeForTest(runner.Manager, pid, creation)
						if err != nil {
							return err
						}
						before, err := observer.Snapshot()
						if err != nil || before.Forced || before.Retired || before.Exited || !before.Registered {
							return errors.Join(fmt.Errorf("invalid live original owner: %+v", before), err)
						}
						if err := task43ExchangeWrite(input, []byte("stop")); err != nil {
							return err
						}
						reply, err := task43ExchangeRead(output)
						if err != nil || string(reply) != "released" {
							return errors.Join(errors.New("missing cooperative release"), err)
						}
						tail, err := io.ReadAll(output)
						if err != nil || len(tail) != 0 {
							return errors.Join(errors.New("cooperative output did not drain cleanly"), err)
						}
						return nil
					}()
				}()
				select {
				case err := <-prepared:
					if err != nil {
						return err
					}
				case <-abort:
					return setupAborted
				}
				ready <- finalization
				select {
				case <-release:
					return nil
				case <-abort:
					return setupAborted
				}
			})
		}()
		defer func() {
			close(abort)
			cancel()
			<-finished
			if ioJoined != nil {
				<-ioJoined
			}
			if observer != nil {
				if err := observer.Close(); err != nil {
					t.Error(err)
				}
			}
			if t.Failed() {
				t.Logf("joined setup/exchange evidence: result=%+v err=%v", result, runErr)
			}
		}()
		var finalization NativeExchangeFinalization
		select {
		case finalization = <-ready:
		case <-finished:
			t.Fatalf("SETUP: exchange ended before cooperative I/O settled: result=%+v err=%v", result, runErr)
		case <-setupWatchdog.C:
			t.Fatal("SETUP: cooperative I/O did not settle; no expiry evidence")
		}
		// Wait cannot return while process/stderr OS waits remain. A stalled
		// Wait remains setup under the suite watchdog, never deadline evidence.
		synctest.Wait()
		select {
		case <-setupWatchdog.C:
			t.Fatal("SETUP: OS settlement exceeded watchdog; no expiry evidence")
		default:
		}
		before, err := observer.Snapshot()
		if err != nil || before.TerminationError != nil || before.Forced || !before.Signaled || !before.Retired || !before.Exited || before.ExitCode != 0 || !before.Registered {
			t.Fatalf("SETUP: original owner did not settle while callback held: %+v %v", before, err)
		}
		requirePending := func(cleanupErr error) {
			t.Helper()
			synctest.Wait()
			if finalization.Control.Err() != nil || control.Err() != nil || finalization.Cleanup.Err() != cleanupErr {
				t.Fatalf("at logical %v: Control=%v caller=%v Cleanup=%v, want Cleanup=%v", time.Now(), finalization.Control.Err(), control.Err(), finalization.Cleanup.Err(), cleanupErr)
			}
			select {
			case <-finished:
				t.Fatalf("exchange returned before callback release: result=%+v err=%v", result, runErr)
			default:
			}
		}
		requirePending(nil)
		// OS settlement is verified above. Separate registration from launch,
		// callback entry and retirement so a timer armed there expires too early.
		time.Sleep(time.Second)
		requirePending(nil)
		// Invoke the actual closure only while its callback is alive. This
		// attests deadline registration, not exact-once registration cardinality.
		// Notification-consumption delay is uncontrolled, so this does not
		// independently prove call-time anchoring versus consumption-time anchoring.
		registeredAt := time.Now()
		finalization.ProtocolDone()
		if after := time.Now(); !after.Equal(registeredAt) {
			t.Fatalf("logical time advanced across actual registration: %v -> %v", registeredAt, after)
		}
		requirePending(nil)
		time.Sleep(time.Until(registeredAt.Add(2*time.Second - time.Nanosecond)))
		requirePending(nil)
		time.Sleep(time.Until(registeredAt.Add(2 * time.Second)))
		requirePending(context.Canceled)
		close(release)
		<-finished
		<-ioJoined
		synctest.Wait()
		if control.Err() != nil || !errors.Is(runErr, ErrNativeExchangeExpired) || !errors.Is(runErr, context.DeadlineExceeded) {
			t.Fatalf("actual registration lost expiry causes: caller=%v result=%+v err=%v", control.Err(), result, runErr)
		}
		after, err := observer.Snapshot()
		if err != nil || after.TerminationError != nil || after.Forced || !after.Signaled || !after.Retired || !after.Exited || after.ExitCode != 0 || after.Registered {
			t.Fatalf("original owner not joined/removed after callback release: %+v %v", after, err)
		}
		t.Logf("actual ProtocolDone at %v; logical expiry at %v; callback and original owner joined: %+v; err=%v", registeredAt, time.Now(), after, runErr)
	})
}
