package service

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
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	secure "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

type task2CorePeerConfig struct {
	Mode    string
	RunID   string
	Start   nativeCoreStart
	Ready   string
	Proceed string
	Closed  string
	Release string
}

// The peer controls only its own protocol endpoints and lifetime. The parent
// callback, its cancel writer, clocks and joins are all production code.
// Close only aliases of this peer's stdin endpoint. Closing os.Stdin alone
// leaves an inherited alias alive in the qualified Windows test PE.
func task2CoreClosePeerInput() error {
	type entry struct {
		value                                    windows.Handle
		handles, pointers                        uintptr
		access, objectType, attributes, reserved uint32
	}
	input := windows.Handle(os.Stdin.Fd())
	// Windows ProcessHandleInformation ABI: phnt/ntpsapi.h. This is a
	// bounded snapshot of CurrentProcess only, never a system handle query.
	buffer := make([]byte, 1<<20)
	var length uint32
	if err := windows.NtQueryInformationProcess(windows.CurrentProcess(), windows.ProcessHandleInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &length); err != nil {
		return err
	}
	header, stride := 2*unsafe.Sizeof(uintptr(0)), unsafe.Sizeof(entry{})
	if uintptr(length) < header || length > uint32(len(buffer)) {
		return errors.New("invalid own handle snapshot length")
	}
	count := *(*uintptr)(unsafe.Pointer(&buffer[0]))
	if count > (uintptr(length)-header)/stride {
		return errors.New("truncated own handle snapshot")
	}
	compare := windows.NewLazySystemDLL("kernelbase.dll").NewProc("CompareObjectHandles")
	sawInput := false
	for i := uintptr(0); i < count; i++ {
		h := (*entry)(unsafe.Pointer(&buffer[header+i*stride])).value
		if h == input {
			sawInput = true
			continue
		}
		equal, _, _ := compare.Call(uintptr(input), uintptr(h))
		if equal != 0 {
			if err := windows.CloseHandle(h); err != nil {
				return err
			}
		}
	}
	if !sawInput {
		return errors.New("stdin missing from own handle snapshot")
	}
	return os.Stdin.Close()
}

func task2CoreFinalizationPeer() error {
	home := os.Getenv("HOME")
	data, err := os.ReadFile(filepath.Join(home, "finalization-peer.json"))
	if err != nil {
		return err
	}
	var config task2CorePeerConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	if config.RunID == "" || os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return errors.New("invalid scripted peer isolation/attempt")
	}
	open := func(name string, access uint32) (windows.Handle, error) {
		ptr, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return 0, err
		}
		return windows.OpenEvent(access, false, ptr)
	}
	ready, err := open(config.Ready, windows.EVENT_MODIFY_STATE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ready)
	proceed, err := open(config.Proceed, windows.SYNCHRONIZE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(proceed)
	closed, err := open(config.Closed, windows.EVENT_MODIFY_STATE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(closed)
	release, err := open(config.Release, windows.SYNCHRONIZE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(release)
	birthIdentity, err := task43CoreBirthOf(windows.CurrentProcess(), uint32(os.Getpid()))
	if err != nil {
		return err
	}
	// A scripted protocol identity, not a claim to run/qualify Core. Test PEs
	// need not expose dependency build metadata; PID and creation are real.
	hello := nativeCoreHello{
		Protocol: 1, CorePath: "github.com/batuta-ai/core", CoreVersion: "v1.1.0-beta.55",
		CoreSum: "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=", PID: birthIdentity.PID, Created: birthIdentity.Created,
	}
	var birth bytes.Buffer
	if err := task43CoreSend(&birth, nativeCoreFrame{Type: "hello", Hello: &hello}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, "finalization-peer-birth"), birth.Bytes(), 0600); err != nil {
		return err
	}
	stream := &nativeCoreStream{reader: os.Stdin, writer: os.Stdout}
	if err := stream.send(nativeCoreFrame{Type: "hello", Hello: &hello}); err != nil {
		return err
	}
	frame, err := stream.receive()
	if err != nil || frame.Type != "start" || frame.Start.validate() != nil || !reflect.DeepEqual(*frame.Start, config.Start) {
		return errors.Join(errors.New("scripted peer start/approval mismatch"), err)
	}
	if err := stream.send(nativeCoreFrame{Type: "ready", ID: 1}); err != nil {
		return err
	}
	frame, err = stream.receive()
	if err != nil || frame.Type != "response" || frame.ID != 1 || frame.Error != nil {
		return errors.Join(errors.New("scripted peer ready rejected"), err)
	}
	if err := windows.SetEvent(ready); err != nil {
		return err
	}
	if wait, err := windows.WaitForSingleObject(proceed, windows.INFINITE); err != nil || wait != windows.WAIT_OBJECT_0 {
		return errors.Join(fmt.Errorf("scripted peer proceed wait=%d", wait), err)
	}
	if config.Mode == "before_done" {
		frame, err = stream.receive()
		if err != nil || frame.Type != "cancel" {
			return errors.Join(errors.New("eligible cancel not received before Done"), err)
		}
	}
	if config.Mode == "ack_write_error" {
		if err := task2CoreClosePeerInput(); err != nil {
			return err
		}
		if err := windows.SetEvent(closed); err != nil {
			return err
		}
		if wait, err := windows.WaitForSingleObject(release, windows.INFINITE); err != nil || wait != windows.WAIT_OBJECT_0 {
			return errors.Join(fmt.Errorf("ACK control release wait=%d", wait), err)
		}
	}
	if err := stream.send(nativeCoreFrame{Type: "done", Done: &nativeCoreDone{State: loop.StateDone}}); err != nil {
		return err
	}
	if config.Mode == "ack_write_error" {
		return os.Stdout.Close()
	}
	frame, err = stream.receive()
	if err != nil || frame.Type != "ack" {
		return errors.Join(errors.New("validated Done ACK missing"), err)
	}
	var extra [1]byte
	if n, err := os.Stdin.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return errors.Join(fmt.Errorf("parent output.Close not attested: bytes=%d", n), err)
	}
	// No output EOF or process return is possible before the parent's release.
	if err := windows.SetEvent(closed); err != nil {
		return err
	}
	if wait, err := windows.WaitForSingleObject(release, windows.INFINITE); err != nil || wait != windows.WAIT_OBJECT_0 {
		return errors.Join(fmt.Errorf("scripted peer release wait=%d", wait), err)
	}
	return os.Stdout.Close()
}

func task2CoreEvent(t *testing.T, name string) windows.Handle {
	t.Helper()
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateEvent(nil, 1, 0, ptr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error(err)
		}
	})
	return h
}

func task2CoreWaitEvent(t *testing.T, h windows.Handle, milestone string) {
	t.Helper()
	// An operational watchdog, never a correctness or scheduling threshold.
	if wait, err := windows.WaitForSingleObject(h, 30000); err != nil || wait != windows.WAIT_OBJECT_0 {
		t.Fatalf("SETUP/UNRELATED: %s wait=%d: %v", milestone, wait, err)
	}
}

// Classify the actual parent endpoint write, not an error-message substring.
// The Done/ACK/input-EOF barriers exclude the preceding start/response/ACK
// writes; on baseline the only remaining write is the joined cancel writer.
func task2CoreClosedCancelError(err error) (closed bool, unrelated bool) {
	if err == nil || err == context.Canceled {
		return false, false
	}
	if path, ok := err.(*os.PathError); ok {
		if path.Op == "write" && path.Path == "|1" && errors.Is(path.Err, os.ErrClosed) {
			return true, false
		}
		return false, true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			c, u := task2CoreClosedCancelError(cause)
			closed, unrelated = closed || c, unrelated || u
		}
		return closed, unrelated
	}
	return false, true
}

func task2CoreParentFinalization(t *testing.T, mode string) {
	t.Helper()
	svc, approval, skills, env, values := task43CoreApprovedFixture(t)
	req := domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID}
	job, err := svc.native.admitExecution(req, batuta.ExecutionDelivery(approval))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	config := task2CorePeerConfig{
		Mode: mode, RunID: job.runID,
		Start:   nativeCoreStart{Receipt: approval, Delivery: job.delivery, Skills: skills, Environment: env, Accounts: values},
		Ready:   `Local\batuta-finalization-ready-` + job.runID,
		Proceed: `Local\batuta-finalization-proceed-` + job.runID,
		Closed:  `Local\batuta-finalization-closed-` + job.runID,
		Release: `Local\batuta-finalization-release-` + job.runID,
	}
	ready := task2CoreEvent(t, config.Ready)
	proceed := task2CoreEvent(t, config.Proceed)
	closed := task2CoreEvent(t, config.Closed)
	release := task2CoreEvent(t, config.Release)
	home := accounts.ExplicitHome(env)
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	task43CoreWriteFile(t, filepath.Join(home, "finalization-peer.json"), string(data))
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	svc.NativeExecutorExecutable = filepath.Join(home, "native-core-finalization-peer.exe")
	task43CoreWriteFile(t, svc.NativeExecutorExecutable, string(data))
	var active atomic.Bool
	var handled bool
	var state string
	var runErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		handled, state, runErr = svc.runNativeWindowsExecution(job, req, approval, "", task43CoreSelected(svc, values), skills, env, &active, func(context.Context) error {
			return errors.New("scripted peer unexpectedly requested a guard")
		})
	}()
	defer func() {
		job.cancel()
		if err := windows.SetEvent(proceed); err != nil {
			t.Error(err)
		}
		if err := windows.SetEvent(release); err != nil {
			t.Error(err)
		}
		// Keep the admitted attempt/resources owned even on an assertion failure.
		// Root's suite watchdog handles a production join that never returns.
		<-finished
	}()
	task2CoreWaitEvent(t, ready, "actual ready response")
	data, err = os.ReadFile(filepath.Join(home, "finalization-peer-birth"))
	if err != nil {
		t.Fatal(err)
	}
	witness, err := task43CoreHello(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(witness)
	if mode == "before_done" {
		job.cancel()
	}
	if err := windows.SetEvent(proceed); err != nil {
		t.Fatal(err)
	}
	task2CoreWaitEvent(t, closed, "peer Done/ACK/input EOF (or closed ACK reader)")
	if !active.Load() || job.request != req || job.runID != config.RunID || job.delivery != config.Start.Delivery {
		t.Fatal("SETUP/UNRELATED: actual parent lost readiness/attempt identity")
	}
	if mode != "ack_write_error" {
		if wait, err := windows.WaitForSingleObject(witness, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("SETUP/UNRELATED: peer exited before release: %d %v", wait, err)
		}
		select {
		case <-finished:
			t.Fatal("SETUP/UNRELATED: parent returned while output/exit held")
		default:
		}
		t.Logf("attempt=%s approval=%s delivery=%s: validated Done/ACK/input EOF; peer output/exit held; writer selection/time unavailable", job.runID, approval.ID, job.delivery)
	}
	if mode == "late_cancel" {
		if err := job.ctx.Err(); err != nil {
			t.Fatalf("SETUP/UNRELATED: Control was already canceled before the late request: %v", err)
		}
		job.cancel()
	}
	if err := windows.SetEvent(release); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("SETUP/UNRELATED: actual parent did not return/join")
	}
	if !handled || state != loop.StateDone {
		t.Fatalf("UNRELATED: actual Done outcome handled=%t state=%q err=%v", handled, state, runErr)
	}
	if wait, err := windows.WaitForSingleObject(witness, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
		t.Fatalf("UNRELATED: parent returned before peer exit: %d %v", wait, err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(witness, &exitCode); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.Session.Shutdown(ctx); err != nil {
		t.Fatalf("UNRELATED: manager joins/cleanup: %v; handler: %v", err, runErr)
	}
	if mode == "ack_write_error" {
		var path *os.PathError
		if !errors.As(runErr, &path) || path.Op != "write" || path.Path != "|1" || !(errors.Is(path.Err, windows.ERROR_BROKEN_PIPE) || errors.Is(path.Err, windows.ERROR_NO_DATA)) {
			t.Fatalf("legitimate ACK write cause lost: %v (exit=%d)", runErr, exitCode)
		}
		if errors.Is(runErr, session.ErrNativeExchangeExpired) || errors.Is(runErr, context.Canceled) || errors.Is(runErr, domain.ErrInvalidReference) || (exitCode != 0 && exitCode != 1) {
			t.Fatalf("ACK write control failed for unrelated reason: %v", runErr)
		}
		return
	}
	closedWrite, unrelated := task2CoreClosedCancelError(runErr)
	if mode == "late_cancel" && closedWrite && !unrelated && errors.Is(runErr, context.Canceled) && (exitCode == 0 || exitCode == 1) {
		t.Fatalf("CAUSAL_LATE_CANCEL_CLOSED_WRITE: Done/ACK/output.Close preceded Control cancel; joined write |1: os.ErrClosed; peer exit=%d; writer selection/time unavailable; err=%v", exitCode, runErr)
	}
	// This peer returns zero; the owned Windows termination path uses code 1.
	// A retained birth-validated handle distinguishes normal return from a kill.
	if exitCode != 0 {
		t.Fatalf("UNRELATED: peer did not exit normally (possible force): %d; %v", exitCode, runErr)
	}
	if mode == "no_cancel" {
		if runErr != nil {
			t.Fatalf("normal finalization failed: %v", runErr)
		}
	} else if !errors.Is(runErr, context.Canceled) || !onlyNativeCancellation(runErr) {
		t.Fatalf("cancellation gained an independent cause: %v", runErr)
	}
	t.Logf("actual handler and manager joined; retained peer exit=0; mode=%s", mode)
}

// Root prescribes exactly 20 independent native samples per side. No local
// repetitions, scheduler barriers, accommodation sleeps or retry-until-RED.
func TestTask2WindowsNativeCoreParentLateControlCancelAfterDone(t *testing.T) {
	task2CoreParentFinalization(t, "late_cancel")
}

func TestTask2WindowsNativeCoreParentDoneWithoutCancel(t *testing.T) {
	task2CoreParentFinalization(t, "no_cancel")
}

func TestTask2WindowsNativeCoreParentCancelBeforeDone(t *testing.T) {
	task2CoreParentFinalization(t, "before_done")
}

func TestTask2WindowsNativeCoreParentPreservesACKWriteError(t *testing.T) {
	task2CoreParentFinalization(t, "ack_write_error")
}

func task43CoreHome(t *testing.T) ([]string, string) {
	t.Helper()
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	return env, home
}

func task43CoreWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := secure.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	f, err := secure.CreatePrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(f, strings.NewReader(body))
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
}

func task43CoreImage(t *testing.T) string {
	t.Helper()
	_, home := task43CoreHome(t)
	image := filepath.Join(home, "consoled.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", image, "./cmd/consoled")
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual consoled build: %v %s", err, out)
	}
	for _, flag := range strings.Fields(os.Getenv("GOFLAGS")) {
		if flag != "-race" {
			continue
		}
		metadata := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "version", "-m", image)
		out, err := metadata.CombinedOutput()
		if err != nil {
			t.Fatalf("actual consoled build metadata: %v %s", err, out)
		}
		instrumented := false
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "build" && fields[1] == "-race=true" {
				instrumented = true
			}
		}
		if !instrumented {
			t.Fatal("contained consoled image lacks requested race instrumentation", string(out))
		}
		t.Log("Actual contained consoled image build metadata: -race=true")
	}
	return image
}

func task43CoreSend(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(data))
	return err
}

func task43CoreReceive(r io.Reader) (map[string]json.RawMessage, error) {
	var size uint32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return nil, err
	}
	if size == 0 || size > 4<<20 {
		return nil, fmt.Errorf("invalid actual protocol frame size %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func task43CoreHello(r io.Reader) (windows.Handle, error) {
	frame, err := task43CoreReceive(r)
	if err != nil {
		return 0, fmt.Errorf("actual whole-Core helper hello: %w", err)
	}
	if string(frame["type"]) != `"hello"` {
		return 0, errors.New("helper did not announce its actual Core dependency")
	}
	var h struct {
		Protocol     int    `json:"protocol"`
		CorePath     string `json:"core_path"`
		CoreVersion  string `json:"core_version"`
		CoreSum      string `json:"core_sum"`
		CoreReplaced bool   `json:"core_replaced"`
		PID          uint32 `json:"pid"`
		Created      uint64 `json:"created"`
	}
	if err := json.Unmarshal(frame["hello"], &h); err != nil {
		return 0, err
	}
	if h.Protocol != 1 || h.CorePath != "github.com/batuta-ai/core" || h.CoreVersion != "v1.1.0-beta.55" || h.CoreSum != "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=" || h.CoreReplaced || h.PID == 0 || h.Created == 0 {
		return 0, fmt.Errorf("unqualified actual linked Core hello %+v", h)
	}
	witness, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, h.PID)
	if err != nil {
		return 0, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(witness, &created, &exited, &kernel, &user); err != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != h.Created {
		windows.CloseHandle(witness)
		return 0, errors.Join(errors.New("original helper birth mismatch"), err)
	}
	return witness, nil
}

func TestTask43WindowsNativeCoreProviderFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-core-provider" {
		return
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		os.Exit(90)
	}
	switch os.Args[4] {
	case "version":
		fmt.Println("codex-cli 0.160.0")
	case "models":
		fmt.Println(`{"models":[{"slug":"qa-native"}]}`)
	case "run":
		file, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "native-launches"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(91)
		}
		fmt.Fprintln(file, "submission")
		if err := file.Close(); err != nil {
			os.Exit(92)
		}
		fmt.Println("BATUTA-QUESTION: which approved output?")
	default:
		os.Exit(93)
	}
	os.Exit(0)
}

func task43CoreApprovedFixture(t *testing.T) (*Service, domain.PlanApproval, string, []string, map[string]nativeBridgeAccount) {
	t.Helper()
	return task43CoreApprovedFixtureWithProof(t, "git diff --check")
}

func task43CoreApprovedFixtureWithProof(t *testing.T, proof string) (*Service, domain.PlanApproval, string, []string, map[string]nativeBridgeAccount) {
	t.Helper()
	_, root := task43CoreHome(t)
	_, tools := task43CoreHome(t)
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(tools, "codex.exe")
	f, err := secure.CreatePrivateFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(data)
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "ambient-must-not-cross-core")
	t.Setenv("ANTHROPIC_API_KEY", "ambient-must-not-cross-core")
	_, skillHome := task43CoreHome(t)
	for _, name := range []string{"batuta", "batuta-loop", "batuta-review", "batuta/adapters"} {
		if err := secure.EnsurePrivateDirectory(filepath.Join(skillHome, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	skills := installedSkillFixture(t, skillHome, "batuta", "batuta-loop", "batuta-review")
	task43CoreWriteFile(t, filepath.Join(skills, "adapters", "codex.md"), "---\nname: codex\nexecutable: codex\nrun: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider run --model {model} \"{brief}\" < /dev/null'\nreadonly: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider review --model {model} \"{prompt}\" < /dev/null'\navailable: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider version'\nmodels: 'codex '-test.run=^TestTask43WindowsNativeCoreProviderFixture$' -- task43-core-provider models'\nmodel_flags: --model {model}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n")
	task43CoreWriteFile(t, filepath.Join(skills, "templates", "generic.md"), "# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n")
	task43CoreWriteFile(t, filepath.Join(root, ".batuta", "profile.md"), "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n")
	task43CoreWriteFile(t, filepath.Join(root, ".batuta", "routing.md"), "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | qa-native |\n| medium | * | codex | qa-native |\n| high | * | codex | qa-native |\n| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | codex | qa-native |\n")
	task43CoreWriteFile(t, filepath.Join(root, ".batuta", "plans", "alpha.md"), "# Plan — Alpha\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → "+proof+"\n")
	task43CoreWriteFile(t, filepath.Join(root, ".gitignore"), ".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "console-native-qa"}, {"config", "user.email", "qa@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "chore: native fixture"}} {
		c := exec.Command(git, args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("native Git fixture: %v %s", err, out)
		}
	}
	svc := setupService(t)
	svc.Session = session.New(root)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: ".batuta/plans/alpha.md"}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := task43CoreHome(t)
	accountEnv, _ := task43CoreHome(t)
	adapter, err := executor.LoadAdapter(skills, "codex")
	if err != nil {
		t.Fatal(err)
	}
	return svc, a, skills, env, map[string]nativeBridgeAccount{"codex": {Executable: cli, Environment: accountEnv, Adapter: adapter, Members: []domain.Member{{ID: "member", CLI: "codex", Model: "qa-native", AccountID: "selected", Maestro: true}}}}
}

func TestTask43WindowsNativeCoreActualQuestionAndRelease(t *testing.T) {
	svc, a, skills, env, selected := task43CoreApprovedFixture(t)
	image := task43CoreImage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var witness windows.Handle
	delivery := batuta.ExecutionDelivery(a)
	var finalState string
	guards, ready := 0, false
	result, err := (session.NativeRunner{Manager: svc.Session, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: a.Identity.Binding.CanonicalRoot}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
		var err error
		witness, err = task43CoreHello(r)
		if err != nil {
			return err
		}
		if err := task43CoreSend(w, map[string]any{"type": "start", "start": map[string]any{"receipt": a, "delivery": delivery, "skills": skills, "environment": env, "accounts": selected}}); err != nil {
			return err
		}
		for {
			frame, err := task43CoreReceive(r)
			if err != nil {
				return err
			}
			switch string(frame["type"]) {
			case `"ready"`:
				if ready {
					return errors.New("duplicate ready")
				}
				ready = true
				if err := task43CoreSend(w, map[string]any{"type": "response", "id": json.RawMessage(frame["id"])}); err != nil {
					return err
				}
			case `"guard"`:
				if !ready {
					return errors.New("effect before parent approval")
				}
				b := a.Identity.Binding
				b.DeliveryID = delivery
				b.OpenedDigest, err = batuta.ValidateApprovedDelivery(b.CanonicalRoot, b, a)
				if err != nil {
					return err
				}
				if err := svc.Store.AttachApprovedExecution(ctx, a.ID, a.Identity, b); err != nil {
					return err
				}
				guards++
				if err := task43CoreSend(w, map[string]any{"type": "response", "id": json.RawMessage(frame["id"])}); err != nil {
					return err
				}
			case `"done"`:
				var done struct {
					State   string           `json:"state"`
					Error   *json.RawMessage `json:"error"`
					Cleanup *json.RawMessage `json:"cleanup"`
				}
				if err := json.Unmarshal(frame["done"], &done); err != nil {
					return err
				}
				if done.Error != nil || done.Cleanup != nil {
					return fmt.Errorf("actual Core failed: %s", frame["done"])
				}
				finalState = done.State
				if err := task43CoreSend(w, map[string]any{"type": "ack"}); err != nil {
					return err
				}
				tail, err := io.ReadAll(r)
				if len(tail) != 0 {
					return errors.New("unframed data after released result")
				}
				return err
			default:
				return fmt.Errorf("unexpected actual helper message %s", frame["type"])
			}
		}
	})
	if witness != 0 {
		defer windows.CloseHandle(witness)
	}
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("real whole-Core run: result=%+v error=%v", result, err)
	}
	if !ready || guards == 0 || finalState != loop.StateWaitingInput {
		t.Fatalf("parent approval/guard/Core state missing ready=%v guards=%d state=%q", ready, guards, finalState)
	}
	if wait, err := windows.WaitForSingleObject(witness, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
		t.Fatal("original helper still alive after Release/ACK/EOF", wait, err)
	}
	view, err := svc.ExecutionState(ctx, "m", "w", a.ID)
	if err != nil || !view.Bound || view.Delivery != delivery || view.OpenedDigest == "" || view.Watch == nil || view.Watch.PendingQuestion == nil {
		t.Fatalf("actual original Store/receipt/question missing %+v %v", view, err)
	}
	launches, err := os.ReadFile(filepath.Join(accounts.ExplicitHome(selected["codex"].Environment), "native-launches"))
	if err != nil || string(launches) != "submission\n" {
		t.Fatalf("selected account submission %q %v", launches, err)
	}
}

func TestTask43WindowsNativeCoreRejectsBeforePreparation(t *testing.T) {
	image := task43CoreImage(t)
	for _, message := range []any{map[string]any{"type": "ack"}, map[string]any{"type": "start", "unexpected": true}, map[string]any{"type": "start", "start": map[string]any{"delivery": "unapproved"}}} {
		env, home := task43CoreHome(t)
		manager := session.New(home)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var witness windows.Handle
		result, runErr := (session.NativeRunner{Manager: manager, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: home}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
			var err error
			witness, err = task43CoreHello(r)
			if err != nil {
				return err
			}
			if err := task43CoreSend(w, message); err != nil {
				return err
			}
			_, err = task43CoreReceive(r)
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("invalid request did not close before preparation: %v", err)
			}
			return nil
		})
		cancel()
		if witness != 0 {
			wait, err := windows.WaitForSingleObject(witness, 0)
			windows.CloseHandle(witness)
			if err != nil || wait != windows.WAIT_OBJECT_0 {
				t.Fatal("rejected helper remained alive", wait, err)
			}
		}
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if runErr != nil || result.ExitCode != 1 {
			t.Fatalf("helper rejection result=%+v err=%v", result, runErr)
		}
		if _, err := os.Stat(filepath.Join(home, ".batuta")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unapproved helper prepared Core state", err)
		}
	}
}

func TestTask43WindowsNativeCoreRejectsOutOfOrderResponse(t *testing.T) {
	image := task43CoreImage(t)
	for _, reply := range []map[string]any{{"type": "response", "id": 2}, {"type": "ack"}} {
		svc, a, skills, env, selected := task43CoreApprovedFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		var witness windows.Handle
		result, err := (session.NativeRunner{Manager: svc.Session, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: a.Identity.Binding.CanonicalRoot}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
			var err error
			witness, err = task43CoreHello(r)
			if err != nil {
				return err
			}
			if err := task43CoreSend(w, map[string]any{"type": "start", "start": map[string]any{"receipt": a, "delivery": batuta.ExecutionDelivery(a), "skills": skills, "environment": env, "accounts": selected}}); err != nil {
				return err
			}
			frame, err := task43CoreReceive(r)
			if err != nil {
				return err
			}
			if string(frame["type"]) != `"ready"` {
				return fmt.Errorf("actual Core readiness missing: %s", frame["type"])
			}
			if err := task43CoreSend(w, reply); err != nil {
				return err
			}
			frame, err = task43CoreReceive(r)
			if err != nil {
				return err
			}
			if string(frame["type"]) != `"done"` || !bytes.Contains(frame["done"], []byte(domain.ErrInvalidReference.Error())) {
				return fmt.Errorf("out-of-order response was not refused: %v", frame)
			}
			tail, err := io.ReadAll(r)
			if len(tail) != 0 {
				return errors.New("unframed output after refused response")
			}
			return err
		})
		cancel()
		if witness != 0 {
			wait, err := windows.WaitForSingleObject(witness, 0)
			windows.CloseHandle(witness)
			if err != nil || wait != windows.WAIT_OBJECT_0 {
				t.Fatal("refused original helper stayed alive", wait, err)
			}
		}
		if err != nil || result.ExitCode != 1 || !bytes.Contains(result.Stderr, []byte(domain.ErrInvalidReference.Error())) {
			t.Fatalf("actual out-of-order result=%+v err=%v", result, err)
		}
		if _, err := os.Stat(filepath.Join(accounts.ExplicitHome(selected["codex"].Environment), "native-launches")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("out-of-order response started selected task", err)
		}
		b, err := svc.planBinding(context.Background(), "m", "w")
		if err != nil || b.DeliveryID != "" || b.OpenedDigest != "" {
			t.Fatal("out-of-order response attached parent delivery", b, err)
		}
	}
}

func TestTask43WindowsNativeCoreRejectsMalformedFrame(t *testing.T) {
	image := task43CoreImage(t)
	framed := func(size uint32, payload string) []byte {
		var b bytes.Buffer
		if err := binary.Write(&b, binary.LittleEndian, size); err != nil {
			t.Fatal(err)
		}
		b.WriteString(payload)
		return b.Bytes()
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"zero", framed(0, "")},
		{"oversized", framed((4<<20)+1, "")},
		{"truncated_header", []byte{1, 0}},
		{"truncated_payload", framed(64, "{")},
		{"invalid_json", framed(1, "{")},
		{"trailing_value", framed(uint32(len(`{"type":"ack"}{}`)), `{"type":"ack"}{}`)},
		{"unknown_type", framed(uint32(len(`{"type":"unknown"}`)), `{"type":"unknown"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, home := task43CoreHome(t)
			manager := session.New(home)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var witness windows.Handle
			result, runErr := (session.NativeRunner{Manager: manager, Environment: env}).Exchange(ctx, publication.Command{Executable: image, Args: []string{"--native-core"}, Directory: home}, func(_ context.Context, r io.Reader, w io.WriteCloser) error {
				var err error
				witness, err = task43CoreHello(r)
				if err != nil {
					return err
				}
				if _, err := io.Copy(w, bytes.NewReader(test.raw)); err != nil {
					return err
				}
				if err := w.Close(); err != nil {
					return err
				}
				tail, err := io.ReadAll(r)
				if len(tail) != 0 {
					return errors.New("malformed frame prepared or emitted Core output")
				}
				return err
			})
			if witness != 0 {
				wait, err := windows.WaitForSingleObject(witness, 0)
				windows.CloseHandle(witness)
				if err != nil || wait != windows.WAIT_OBJECT_0 {
					t.Fatal("malformed-frame helper remained alive", wait, err)
				}
			}
			if err := manager.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if runErr != nil || result.ExitCode != 1 || len(result.Stderr) == 0 {
				t.Fatalf("malformed frame result=%+v error=%v", result, runErr)
			}
			if _, err := os.Stat(filepath.Join(home, ".batuta")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("malformed frame prepared unapproved Core state", err)
			}
		})
	}
}

func TestTask43WindowsNativeCoreErrorCauses(t *testing.T) {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	var restricted windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), event, windows.CurrentProcess(), &restricted, windows.EVENT_MODIFY_STATE, false, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(restricted)
	_, kernel := windows.WaitForSingleObject(restricted, 0)
	if !errors.Is(kernel, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("actual restricted event did not produce access error", kernel)
	}
	for _, test := range []struct {
		name   string
		cause  error
		wanted []error
	}{
		{"invalid", errors.Join(domain.ErrInvalidReference, kernel), []error{domain.ErrInvalidReference, kernel}},
		{"binding", fmt.Errorf("child binding: %w", errors.Join(domain.ErrBindingConflict, kernel)), []error{domain.ErrBindingConflict, kernel}},
		{"review_cleanup", errors.Join(publication.ErrReviewCleanupUnresolved, session.ErrNativeCleanupUnresolved, kernel), []error{publication.ErrReviewCleanupUnresolved, session.ErrNativeCleanupUnresolved, kernel}},
		{"uncertain_cancel", errors.Join(domain.ErrExecutionUncertain, context.Canceled, context.DeadlineExceeded, kernel), []error{domain.ErrExecutionUncertain, context.Canceled, context.DeadlineExceeded, kernel}},
		{"deduplicated_kernel", errors.Join(kernel, fmt.Errorf("second cause: %w", kernel)), []error{kernel}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wire bytes.Buffer
			stream := &nativeCoreStream{reader: &wire, writer: &wire}
			done := &nativeCoreDone{State: loop.StateReviewBlocked, Error: nativeCoreEncodeError(test.cause), Cleanup: nativeCoreEncodeError(test.cause)}
			if err := stream.send(nativeCoreFrame{Type: "done", Done: done}); err != nil {
				t.Fatal(err)
			}
			frame, err := stream.receive()
			if err != nil || frame.Type != "done" || frame.Done == nil || frame.Done.Cleanup == nil {
				t.Fatal("real DONE cleanup codec", frame, err)
			}
			for _, encoded := range []*nativeCoreError{frame.Done.Error, frame.Done.Cleanup} {
				if encoded.Message != test.cause.Error() || len(encoded.Kernel) != 1 || encoded.Kernel[0] != uint32(windows.ERROR_ACCESS_DENIED) {
					t.Fatal("lost message or duplicate actual kernel cause", encoded)
				}
				for _, wanted := range test.wanted {
					if !errors.Is(encoded.cause(), wanted) {
						t.Fatalf("DONE Error/Cleanup lost actual cause %v: %v", wanted, encoded.cause())
					}
				}
			}
		})
	}
}

func TestTask4WindowsNativeCoreDoneRegistrationBeforeFinalDrain(t *testing.T) {
	svc, approval, skills, env, values := task43CoreApprovedFixture(t)
	req := domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID}
	job, err := svc.native.admitExecution(req, batuta.ExecutionDelivery(approval))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.native.finish(job)
	config := task2CorePeerConfig{
		Mode: "no_cancel", RunID: job.runID,
		Start:   nativeCoreStart{Receipt: approval, Delivery: job.delivery, Skills: skills, Environment: env, Accounts: values},
		Ready:   `Local\batuta-task4-ready-` + job.runID,
		Proceed: `Local\batuta-task4-proceed-` + job.runID,
		Closed:  `Local\batuta-task4-closed-` + job.runID,
		Release: `Local\batuta-task4-release-` + job.runID,
	}
	ready := task2CoreEvent(t, config.Ready)
	proceed := task2CoreEvent(t, config.Proceed)
	closed := task2CoreEvent(t, config.Closed)
	release := task2CoreEvent(t, config.Release)
	home := accounts.ExplicitHome(env)
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	task43CoreWriteFile(t, filepath.Join(home, "finalization-peer.json"), string(data))
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	svc.NativeExecutorExecutable = filepath.Join(home, "native-core-finalization-peer.exe")
	task43CoreWriteFile(t, svc.NativeExecutorExecutable, string(data))
	selected := task43CoreSelected(svc, values)
	var active atomic.Bool
	var handled bool
	var state string
	var runErr error
	var witness windows.Handle
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		handled, state, runErr = svc.runNativeWindowsExecution(job, req, approval, "", selected, skills, env, &active, func(context.Context) error {
			return errors.New("scripted peer unexpectedly requested a guard")
		})
	}()
	defer func() {
		for _, barrier := range []windows.Handle{release, proceed} {
			if err := windows.SetEvent(barrier); err != nil {
				t.Error(err)
			}
		}
		job.cancel()
		// Preserve attempt, peer events and witness until every production join
		// returns, including on failure; a broken join belongs to the suite watchdog.
		<-finished
		if witness != 0 {
			if err := windows.CloseHandle(witness); err != nil {
				t.Error(err)
			}
		}
		if t.Failed() {
			t.Logf("joined attempt=%s handled=%t state=%q err=%v", job.runID, handled, state, runErr)
		}
	}()
	task2CoreWaitEvent(t, ready, "actual ready response before held final drain")
	data, err = os.ReadFile(filepath.Join(home, "finalization-peer-birth"))
	if err != nil {
		t.Fatal(err)
	}
	witness, err = task43CoreHello(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !active.Load() || job.request != req || job.runID != config.RunID || job.delivery != config.Start.Delivery || job.ctx.Err() != nil {
		t.Fatalf("SETUP: actual readiness/attempt/Control changed: active=%t Control=%v", active.Load(), job.ctx.Err())
	}
	if wait, err := windows.WaitForSingleObject(witness, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("SETUP: original peer not alive before Done: wait=%d err=%v", wait, err)
	}
	if err := windows.SetEvent(proceed); err != nil {
		t.Fatal(err)
	}
	task2CoreWaitEvent(t, closed, "peer validated Done/ACK/input EOF with output/exit held")
	// This proves registration before final drain completes, not before ACK:
	// registration immediately after ACK would satisfy the same observation.
	if job.ctx.Err() != nil {
		t.Fatalf("Control canceled before observing held final drain: %v", job.ctx.Err())
	}
	if wait, err := windows.WaitForSingleObject(release, 0); err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("peer output/exit release barrier changed: wait=%d err=%v", wait, err)
	}
	t.Logf("attempt=%s approval=%s delivery=%s: Done/ACK/input EOF; release unset; Control live", job.runID, approval.ID, job.delivery)
	watchdog := time.NewTimer(30 * time.Second)
	defer watchdog.Stop()
	select {
	case <-finished:
	case <-watchdog.C:
		t.Fatal("actual Service did not join while final output/exit held; operational watchdog, not deadline measurement")
	}
	if job.ctx.Err() != nil || !handled || state != loop.StateDone || !errors.Is(runErr, session.ErrNativeExchangeExpired) || !errors.Is(runErr, context.DeadlineExceeded) {
		t.Fatalf("Service lost validated Done/expiry with live Control: Control=%v handled=%t state=%q err=%v", job.ctx.Err(), handled, state, runErr)
	}
	if wait, err := windows.WaitForSingleObject(witness, 0); err != nil || wait != windows.WAIT_OBJECT_0 {
		t.Fatalf("Service returned before original peer was signaled: wait=%d err=%v; handler=%v", wait, err, runErr)
	}
	t.Logf("joined actual Service with validated Done, held release and signaled original peer: state=%q err=%v", state, runErr)
}

// These units call the complete production callback. Task1 tests the concrete
// registrator; the unchanged real Exchange/Service tests establish its wiring.
type nativeCoreCallbackObservation struct {
	registrations int
	ackEntries    int
	atACK         int
	closes        int
	drains        int
	frames        []nativeCoreFrame
	events        []string
}

type nativeCoreCallbackLog struct {
	mu sync.Mutex
	nativeCoreCallbackObservation
}

func (l *nativeCoreCallbackLog) snapshot() nativeCoreCallbackObservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	got := l.nativeCoreCallbackObservation
	got.frames = append([]nativeCoreFrame(nil), got.frames...)
	got.events = append([]string(nil), got.events...)
	return got
}

type nativeCoreCallbackBarrier struct {
	channel chan struct{}
	once    sync.Once
}

func newNativeCoreCallbackBarrier() *nativeCoreCallbackBarrier {
	return &nativeCoreCallbackBarrier{channel: make(chan struct{})}
}

func (b *nativeCoreCallbackBarrier) release() {
	b.once.Do(func() { close(b.channel) })
}

type nativeCoreCallbackOptions struct {
	phases    []string
	ackErr    error
	cancelErr error
	closeErr  error
	drainErr  error
	tail      []byte
	guardErr  error
}

type nativeCoreCallbackWriter struct {
	log           *nativeCoreCallbackLog
	options       nativeCoreCallbackOptions
	ackRelease    *nativeCoreCallbackBarrier
	cancelRelease *nativeCoreCallbackBarrier
	phase         int
	remaining     uint32
	wire          bytes.Buffer
}

func (w *nativeCoreCallbackWriter) Write(p []byte) (int, error) {
	if w.phase >= len(w.options.phases) {
		return 0, errors.New("unexpected output after final callback phase")
	}
	phase := w.options.phases[w.phase]
	if w.remaining == 0 {
		if len(p) != 4 {
			return 0, fmt.Errorf("%s header Write length = %d, want 4", phase, len(p))
		}
		size := binary.LittleEndian.Uint32(p)
		if size == 0 || size > nativeCoreFrameLimit {
			return 0, fmt.Errorf("%s header has invalid size %d", phase, size)
		}
		if phase == "ack" {
			ack, err := json.Marshal(nativeCoreFrame{Type: "ack"})
			if err != nil || size != uint32(len(ack)) {
				return 0, fmt.Errorf("actual ACK header size = %d, want %d: %v", size, len(ack), err)
			}
			w.log.mu.Lock()
			w.log.ackEntries++
			w.log.atACK = w.log.registrations
			w.log.events = append(w.log.events, "ack-header")
			w.log.mu.Unlock()
			// No observation mutex is held while the controller releases Write.
			<-w.ackRelease.channel
			if w.options.ackErr != nil {
				return 0, w.options.ackErr
			}
		}
		w.remaining = size
		w.wire.Write(p)
		return len(p), nil
	}
	if uint32(len(p)) > w.remaining {
		return 0, fmt.Errorf("%s payload exceeds its decoded header", phase)
	}
	w.wire.Write(p)
	w.remaining -= uint32(len(p))
	if w.remaining != 0 {
		return len(p), nil
	}
	frame, err := (&nativeCoreStream{reader: bytes.NewReader(w.wire.Bytes())}).receive()
	if err != nil || frame.Type != phase {
		return 0, fmt.Errorf("decoded output phase = %q, want %q: %v", frame.Type, phase, err)
	}
	w.wire.Reset()
	w.phase++
	w.log.mu.Lock()
	w.log.frames = append(w.log.frames, frame)
	w.log.events = append(w.log.events, frame.Type)
	w.log.mu.Unlock()
	if phase == "cancel" {
		// The controller observes the decoded cancel before feeding Done, but
		// can retain this actual writer call to prove the callback's join.
		<-w.cancelRelease.channel
		if w.options.cancelErr != nil {
			return len(p), w.options.cancelErr
		}
	}
	return len(p), nil
}

func (w *nativeCoreCallbackWriter) Close() error {
	w.log.mu.Lock()
	w.log.closes++
	w.log.events = append(w.log.events, "close")
	w.log.mu.Unlock()
	return w.options.closeErr
}

type nativeCoreCallbackInput struct {
	wire  []byte
	final bool
}

type nativeCoreCallbackReader struct {
	log          *nativeCoreCallbackLog
	options      nativeCoreCallbackOptions
	inputs       chan nativeCoreCallbackInput
	stop         *nativeCoreCallbackBarrier
	drainRelease *nativeCoreCallbackBarrier
	current      *bytes.Reader
	final        bool
	drained      bool
}

func (r *nativeCoreCallbackReader) Read(p []byte) (int, error) {
	if r.current == nil || r.current.Len() == 0 {
		if r.final {
			if r.drained {
				return 0, io.EOF
			}
			r.drained = true
			r.log.mu.Lock()
			r.log.drains++
			r.log.events = append(r.log.events, "drain")
			r.log.mu.Unlock()
			<-r.drainRelease.channel
			r.current = bytes.NewReader(r.options.tail)
			if r.current.Len() == 0 {
				if r.options.drainErr != nil {
					return 0, r.options.drainErr
				}
				return 0, io.EOF
			}
		} else {
			select {
			case input := <-r.inputs:
				r.current, r.final = bytes.NewReader(input.wire), input.final
			case <-r.stop.channel:
				return 0, io.EOF
			}
		}
	}
	return r.current.Read(p)
}

type nativeCoreCallbackHarness struct {
	log           *nativeCoreCallbackLog
	input         *nativeCoreCallbackReader
	output        *nativeCoreCallbackWriter
	control       context.Context
	cancel        context.CancelFunc
	active        atomic.Bool
	joined        chan struct{}
	report        nativeCoreCallbackReport
	err           error
	start         nativeCoreStart
	guardContexts chan context.Context
}

func newNativeCoreCallbackHarness(options nativeCoreCallbackOptions) *nativeCoreCallbackHarness {
	control, cancel := context.WithCancel(context.Background())
	log := &nativeCoreCallbackLog{}
	h := &nativeCoreCallbackHarness{
		log: log, control: control, cancel: cancel, joined: make(chan struct{}),
		// The outer function validates Start. A nonempty resume selects the
		// callback's existing readiness path without a Store/provider fixture.
		start:         nativeCoreStart{Delivery: "callback-delivery", Resume: "callback-delivery"},
		guardContexts: make(chan context.Context, 1),
	}
	h.input = &nativeCoreCallbackReader{log: log, options: options, inputs: make(chan nativeCoreCallbackInput, 1), stop: newNativeCoreCallbackBarrier(), drainRelease: newNativeCoreCallbackBarrier()}
	h.output = &nativeCoreCallbackWriter{log: log, options: options, ackRelease: newNativeCoreCallbackBarrier(), cancelRelease: newNativeCoreCallbackBarrier()}
	finalization := session.NativeExchangeFinalization{
		Control: control, Cleanup: context.Background(),
		ProtocolDone: func() {
			log.mu.Lock()
			log.registrations++
			log.events = append(log.events, "protocol-done")
			log.mu.Unlock()
		},
	}
	go func() {
		defer close(h.joined)
		h.report, h.err = (&Service{}).runNativeCoreCallback(finalization, h.input, h.output, h.start, domain.ExecutionRequest{}, h.start.Resume, nativeAccounts{}, &h.active, func(ctx context.Context) error {
			h.guardContexts <- ctx
			return options.guardErr
		})
	}()
	return h
}

func (h *nativeCoreCallbackHarness) finish() {
	h.output.ackRelease.release()
	h.output.cancelRelease.release()
	h.input.drainRelease.release()
	h.input.stop.release()
	h.cancel()
	<-h.joined
}

func (h *nativeCoreCallbackHarness) feed(t *testing.T, frame nativeCoreFrame, final bool) {
	t.Helper()
	var wire bytes.Buffer
	// Raw framing permits deliberately invalid envelopes to reach receive().
	if err := task43CoreSend(&wire, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case h.input.inputs <- nativeCoreCallbackInput{wire: wire.Bytes(), final: final}:
	case <-h.joined:
		t.Fatalf("callback returned before input %s: %v", frame.Type, h.err)
	}
	synctest.Wait()
}

func (h *nativeCoreCallbackHarness) expectFrames(t *testing.T, kinds ...string) {
	t.Helper()
	got := h.log.snapshot()
	var names []string
	for _, frame := range got.frames {
		names = append(names, frame.Type)
	}
	if !reflect.DeepEqual(names, kinds) {
		t.Fatalf("decoded callback phases = %v, want %v", names, kinds)
	}
}

func (h *nativeCoreCallbackHarness) hello(t *testing.T) {
	t.Helper()
	hello := nativeCoreHello{Protocol: 1, PID: 1, Created: 1, CorePath: "github.com/batuta-ai/core", CoreVersion: "v1.1.0-beta.55", CoreSum: "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y="}
	h.feed(t, nativeCoreFrame{Type: "hello", Hello: &hello}, false)
	h.expectFrames(t, "start")
	if got := h.log.snapshot().frames[0].Start; got == nil || !reflect.DeepEqual(*got, h.start) {
		t.Fatalf("actual start frame = %+v, want %+v", got, h.start)
	}
}

func (h *nativeCoreCallbackHarness) ready(t *testing.T) {
	t.Helper()
	h.feed(t, nativeCoreFrame{Type: "ready", ID: 1}, false)
	h.expectFrames(t, "start", "response")
	frame := h.log.snapshot().frames[1]
	if frame.ID != 1 || frame.Error != nil || !h.active.Load() {
		t.Fatalf("actual ready response = %+v, active=%t", frame, h.active.Load())
	}
}

func (h *nativeCoreCallbackHarness) expectPending(t *testing.T, phase string) {
	t.Helper()
	select {
	case <-h.joined:
		t.Fatalf("callback returned while %s held: %v", phase, h.err)
	default:
	}
}

func (h *nativeCoreCallbackHarness) expectACKHeld(t *testing.T) {
	t.Helper()
	synctest.Wait()
	got := h.log.snapshot()
	if got.ackEntries != 1 {
		t.Fatalf("actual ACK header entries = %d, want 1; events=%v", got.ackEntries, got.events)
	}
	// Frozen counterproof markers: afterACK, afterClose/drain and duplicate.
	if got.atACK != 1 {
		t.Errorf("ProtocolDone calls at actual ACK header entry = %d, want exactly 1", got.atACK)
	}
	if got.registrations != 1 {
		t.Errorf("ProtocolDone calls while actual ACK header held = %d, want exactly 1", got.registrations)
	}
	if got.closes != 0 || got.drains != 0 {
		t.Errorf("Close/drain before ACK release = %d/%d, want 0/0", got.closes, got.drains)
	}
	h.expectPending(t, "actual ACK header Write")
}

func (h *nativeCoreCallbackHarness) releaseACKToDrain(t *testing.T) {
	t.Helper()
	h.output.ackRelease.release()
	synctest.Wait()
	got := h.log.snapshot()
	if got.closes != 1 || got.drains != 1 {
		t.Fatalf("Close/drain after ACK release = %d/%d, want 1/1; events=%v", got.closes, got.drains, got.events)
	}
	if len(got.events) < 4 || !reflect.DeepEqual(got.events[len(got.events)-4:], []string{"ack-header", "ack", "close", "drain"}) {
		t.Errorf("actual ACK -> Close -> drain order = %v", got.events)
	}
	if got.frames[len(got.frames)-1].Type != "ack" {
		t.Error("held header was not followed by the decoded actual ACK")
	}
	h.expectPending(t, "tail drain")
	h.input.drainRelease.release()
}

func (h *nativeCoreCallbackHarness) result(t *testing.T) (nativeCoreCallbackReport, error) {
	t.Helper()
	synctest.Wait()
	select {
	case <-h.joined:
		return h.report, h.err
	default:
		t.Fatal("callback did not join after all required releases")
		return nativeCoreCallbackReport{}, nil
	}
}

func TestNativeCoreCallbackProtocolDoneBeforeACK(t *testing.T) {
	for _, name := range []string{"noStop", "eligibleCancel", "errorBeforeReady"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				phases := []string{"start"}
				if name != "errorBeforeReady" {
					phases = append(phases, "response")
				}
				if name == "eligibleCancel" {
					phases = append(phases, "cancel")
				}
				phases = append(phases, "ack")
				h := newNativeCoreCallbackHarness(nativeCoreCallbackOptions{phases: phases})
				defer h.finish()
				h.hello(t)
				if name != "errorBeforeReady" {
					h.ready(t)
				}
				if name == "eligibleCancel" {
					h.cancel()
					synctest.Wait()
					h.expectFrames(t, "start", "response", "cancel")
					h.output.cancelRelease.release()
				}
				done := &nativeCoreDone{State: loop.StateDone}
				if name == "errorBeforeReady" {
					done.State, done.Error = "", nativeCoreEncodeError(context.Canceled)
				}
				h.feed(t, nativeCoreFrame{Type: "done", Done: done}, true)
				h.expectACKHeld(t)
				h.releaseACKToDrain(t)
				report, err := h.result(t)
				if err != nil || !report.completed || report.state != done.State || report.parentCause != nil || report.cleanup != nil {
					t.Errorf("accepted callback report = %+v, err=%v", report, err)
				}
				if name == "errorBeforeReady" {
					if !errors.Is(report.outcome, context.Canceled) || h.active.Load() {
						t.Errorf("allowed pre-ready error lost: report=%+v active=%t", report, h.active.Load())
					}
				} else if report.outcome != nil {
					t.Errorf("unexpected accepted outcome: %v", report.outcome)
				}
				if name != "eligibleCancel" && h.control.Err() != nil {
					t.Errorf("noStop control changed: %v", h.control.Err())
				}
				h.expectFrames(t, phases...)
				if got := h.log.snapshot().registrations; got != 1 {
					t.Errorf("ProtocolDone calls after callback join = %d, want exactly 1", got)
				}
			})
		})
	}
}

func TestNativeCoreCallbackRejectsUnvalidatedDone(t *testing.T) {
	for _, test := range []struct {
		name  string
		ready bool
		frame nativeCoreFrame
	}{
		{"invalidEnvelope", true, nativeCoreFrame{Type: "done", Done: &nativeCoreDone{State: loop.StateDone}, Guard: &nativeCoreGuard{}}},
		{"invalidDoneID", true, nativeCoreFrame{Type: "done", ID: 1, Done: &nativeCoreDone{State: loop.StateDone}}},
		{"doneBeforeReady", false, nativeCoreFrame{Type: "done", Done: &nativeCoreDone{State: loop.StateDone}}},
		{"invalidState", true, nativeCoreFrame{Type: "done", Done: &nativeCoreDone{State: "unvalidated-state"}}},
		{"invalidReadyID", false, nativeCoreFrame{Type: "ready", ID: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				phases := []string{"start"}
				if test.ready {
					phases = append(phases, "response")
				}
				h := newNativeCoreCallbackHarness(nativeCoreCallbackOptions{phases: phases})
				defer h.finish()
				h.hello(t)
				if test.ready {
					h.ready(t)
				}
				h.feed(t, test.frame, true)
				report, err := h.result(t)
				if !errors.Is(err, domain.ErrInvalidReference) || report.completed || report.state != "" || report.outcome != nil || report.parentCause != nil || report.cleanup != nil {
					t.Errorf("invalid input report = %+v, err=%v", report, err)
				}
				got := h.log.snapshot()
				// Frozen counterproof marker: registration before state validation.
				if got.registrations != 0 {
					t.Errorf("ProtocolDone calls for unvalidated Done = %d, want 0", got.registrations)
				}
				if got.ackEntries != 0 || got.closes != 0 || got.drains != 0 {
					t.Errorf("invalid input reached ACK/Close/drain: %+v", got)
				}
				h.expectFrames(t, phases...)
			})
		})
	}
}

func TestNativeCoreCallbackPreservesAcceptedReport(t *testing.T) {
	for _, name := range []string{"ackFailure", "closeFailure", "drainFailure", "trailingData"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sentinel := errors.New("callback " + name)
				options := nativeCoreCallbackOptions{phases: []string{"start", "response", "response", "ack"}, guardErr: domain.ErrBindingConflict}
				switch name {
				case "ackFailure":
					options.ackErr = sentinel
				case "closeFailure":
					options.closeErr = sentinel
				case "drainFailure":
					options.drainErr = sentinel
				case "trailingData":
					options.tail = []byte("unexpected tail")
					sentinel = domain.ErrInvalidReference
				}
				h := newNativeCoreCallbackHarness(options)
				defer h.finish()
				h.hello(t)
				h.ready(t)
				h.feed(t, nativeCoreFrame{Type: "guard", ID: 2, Guard: &nativeCoreGuard{}}, false)
				h.expectFrames(t, "start", "response", "response")
				response := h.log.snapshot().frames[2]
				if response.ID != 2 || !errors.Is(response.Error.cause(), domain.ErrBindingConflict) {
					t.Fatalf("actual guard response = %+v", response)
				}
				select {
				case ctx := <-h.guardContexts:
					if ctx != h.control {
						t.Fatal("guard lost actual Control context")
					}
				default:
					t.Fatal("guard callback not called")
				}
				done := &nativeCoreDone{State: loop.StateCanceled, Error: nativeCoreEncodeError(context.Canceled), Cleanup: nativeCoreEncodeError(session.ErrNativeCleanupUnresolved)}
				h.feed(t, nativeCoreFrame{Type: "done", Done: done}, true)
				h.expectACKHeld(t)
				if name == "drainFailure" || name == "trailingData" {
					h.releaseACKToDrain(t)
				} else {
					h.output.ackRelease.release()
				}
				report, err := h.result(t)
				if !errors.Is(err, sentinel) || !report.completed || report.state != loop.StateCanceled || !errors.Is(report.outcome, context.Canceled) || !errors.Is(report.parentCause, domain.ErrBindingConflict) || !errors.Is(report.cleanup, session.ErrNativeCleanupUnresolved) {
					t.Errorf("accepted report lost on %s: report=%+v err=%v", name, report, err)
				}
				got := h.log.snapshot()
				wantCloses, wantDrains := 1, 1
				if name == "ackFailure" {
					wantCloses, wantDrains = 0, 0
				} else if name == "closeFailure" {
					wantDrains = 0
				}
				if got.registrations != 1 || got.closes != wantCloses || got.drains != wantDrains {
					t.Errorf("post-error registration/Close/drain = %d/%d/%d, want 1/%d/%d", got.registrations, got.closes, got.drains, wantCloses, wantDrains)
				}
			})
		})
	}
}

func TestNativeCoreCallbackJoinsCancelWriter(t *testing.T) {
	for _, name := range []string{"ackFailure", "invalidState"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cancelErr, ackErr := errors.New("independent cancel writer failure"), errors.New("independent ACK failure")
				h := newNativeCoreCallbackHarness(nativeCoreCallbackOptions{phases: []string{"start", "response", "cancel", "ack"}, cancelErr: cancelErr, ackErr: ackErr})
				defer h.finish()
				h.hello(t)
				h.ready(t)
				h.cancel()
				synctest.Wait()
				h.expectFrames(t, "start", "response", "cancel")
				done := &nativeCoreDone{State: loop.StateDone}
				if name == "invalidState" {
					done.State = "unvalidated-state"
				}
				h.feed(t, nativeCoreFrame{Type: "done", Done: done}, true)
				h.expectPending(t, "decoded cancel payload Write")
				got := h.log.snapshot()
				wantCalls := 1
				if name == "invalidState" {
					wantCalls = 0
				}
				if got.registrations != wantCalls || got.ackEntries != 0 || got.closes != 0 || got.drains != 0 {
					t.Errorf("cancel writer join ordering: got=%+v, want calls=%d and no ACK/Close/drain", got, wantCalls)
				}
				h.output.cancelRelease.release()
				if name == "ackFailure" {
					h.expectACKHeld(t)
					h.output.ackRelease.release()
				}
				report, err := h.result(t)
				wanted := ackErr
				if name == "invalidState" {
					wanted = domain.ErrInvalidReference
				}
				if !errors.Is(err, cancelErr) || !errors.Is(err, wanted) {
					t.Errorf("joined callback lost independent cancel writer error: %v", err)
				}
				if name == "ackFailure" {
					if !report.completed || report.state != loop.StateDone {
						t.Errorf("cancel/ACK failure lost accepted report: %+v", report)
					}
				} else if report.completed || report.state != "" {
					t.Errorf("invalid state accepted after cancel writer join: %+v", report)
				}
				if report.outcome != nil || report.parentCause != nil || report.cleanup != nil {
					t.Errorf("unexpected cancel writer report causes: %+v", report)
				}
				got = h.log.snapshot()
				if got.registrations != wantCalls || got.closes != 0 || got.drains != 0 || name == "invalidState" && got.ackEntries != 0 {
					t.Errorf("cancel writer failure reached unexpected finalization: %+v", got)
				}
			})
		})
	}
}
