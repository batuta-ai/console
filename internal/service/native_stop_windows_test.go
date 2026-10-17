//go:build windows

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"golang.org/x/sys/windows"
)

// Dispatch is not a test case: a normal test run cannot count this provider as
// qualification. The existing HoldPE and TestMain remain byte-for-byte intact.
func init() {
	if len(os.Args) > 3 && os.Args[2] == "--" && os.Args[3] == "native-stop-provider" {
		if err := nativeStopWindowsProvider(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
}

func nativeStopWindowsEvent(root string) string {
	return fmt.Sprintf("Local\\BatutaNativeStop-%x", sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root)))))
}

type nativeStopWindowsBirths struct{ Root, Child, Core task43CoreBirth }

func nativeStopWindowsCoreBirth() (task43CoreBirth, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return task43CoreBirth{}, err
	}
	defer windows.CloseHandle(snapshot)
	entries := map[uint32]windows.ProcessEntry32{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		entries[entry.ProcessID] = entry
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return task43CoreBirth{}, err
	}
	pid := uint32(os.Getpid())
	for depth := 0; depth < 32; depth++ {
		entry, ok := entries[pid]
		if !ok {
			break
		}
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "consoled.exe") {
			handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil {
				return task43CoreBirth{}, err
			}
			defer windows.CloseHandle(handle)
			return task43CoreBirthOf(handle, pid)
		}
		pid = entry.ParentProcessID
	}
	return task43CoreBirth{}, errors.New("actual public Core helper ancestor not observed")
}

func nativeStopWindowsProvider() error {
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return errors.New("ambient credentials crossed provider boundary")
	}
	if len(os.Args) < 5 {
		return errors.New("missing provider role")
	}
	switch os.Args[4] {
	case "version":
		fmt.Println("codex-cli 0.160.0")
		return nil
	case "models":
		fmt.Println(`{"models":[{"slug":"qa-native"}]}`)
		return nil
	case "review":
		fmt.Println("<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE")
		return nil
	case "run":
	default:
		return errors.New("unqualified provider role")
	}
	if len(os.Args) < 8 || os.Args[5] != "--model" || os.Args[6] != "qa-native" {
		return errors.New("unbound provider model")
	}
	if strings.Contains(os.Args[len(os.Args)-1], "BATUTA-CAPABLE") {
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return err
		}
		fmt.Printf("BATUTA-CAPABLE %s\n", strings.TrimSpace(string(out)))
		return nil
	}
	out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return err
	}
	common := strings.TrimSpace(string(out))
	eventName, err := windows.UTF16PtrFromString(nativeStopWindowsEvent(common))
	if err != nil {
		return err
	}
	event, err := windows.CreateEvent(nil, 1, 0, eventName)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	var inJob int32
	result, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&inJob)))
	if result == 0 || inJob == 0 {
		return fmt.Errorf("provider is outside actual native Job: %v", callErr)
	}
	child := exec.Command(os.Args[0], "--task43-core-hold")
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return err
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	root, err := task43CoreBirthOf(windows.CurrentProcess(), uint32(os.Getpid()))
	if err != nil {
		return err
	}
	var born task43CoreBirth
	var birthErr error
	if err := child.Process.WithHandle(func(h uintptr) { born, birthErr = task43CoreBirthOf(windows.Handle(h), uint32(child.Process.Pid)) }); err != nil {
		return err
	}
	if birthErr != nil {
		return birthErr
	}
	core, err := nativeStopWindowsCoreBirth()
	if err != nil {
		return err
	}
	data, err := json.Marshal(nativeStopWindowsBirths{Root: root, Child: born, Core: core})
	if err != nil {
		return err
	}
	marker := filepath.Join(common, "native-stop-birth.json")
	if err := os.WriteFile(marker+".tmp", data, 0600); err != nil {
		return err
	}
	if err := os.Rename(marker+".tmp", marker); err != nil {
		return err
	}
	status, err := windows.WaitForSingleObject(event, windows.INFINITE)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("release event: %d %v", status, err)
	}
	if err := os.WriteFile("out.txt", []byte("approved output\n"), 0600); err != nil {
		return err
	}
	fmt.Println("completed")
	return nil
}

func nativeStopWindowsImage(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("native source root unavailable")
	}
	_, home := task43CoreHome(t)
	image := filepath.Join(home, "consoled.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", image, "./cmd/consoled")
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual public helper build: %v %s", err, out)
	}
	return image
}

func nativeStopWindowsTree(t *testing.T, ctx context.Context, a domain.PlanApproval) []windows.Handle {
	t.Helper()
	var tree nativeStopWindowsBirths
	nativeStopAwait(t, ctx, "actual provider birth handles", func() bool {
		data, err := os.ReadFile(filepath.Join(a.Identity.Binding.CanonicalRoot, ".git", "native-stop-birth.json"))
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &tree); err != nil {
			t.Fatal(err)
		}
		return true
	})
	var handles []windows.Handle
	for _, birth := range []task43CoreBirth{tree.Root, tree.Child, tree.Core} {
		h, err := task43CoreWitness(birth)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
		t.Cleanup(func() {
			if err := windows.CloseHandle(h); err != nil {
				t.Error(err)
			}
		})
	}
	return handles
}

func nativeStopWindowsHandles(t *testing.T, handles []windows.Handle, alive bool) {
	t.Helper()
	want := uint32(windows.WAIT_OBJECT_0)
	if alive {
		want = uint32(windows.WAIT_TIMEOUT)
	}
	for _, h := range handles {
		status, err := windows.WaitForSingleObject(h, 0)
		if err != nil || status != want {
			t.Fatalf("original process handle %v: wait=%d want=%d err=%v", h, status, want, err)
		}
	}
}

func nativeStopWindowsJournalLock(t *testing.T, a domain.PlanApproval, delivery string) func() {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(a.Identity.Binding.CanonicalRoot, ".batuta", "journal", delivery+".jsonl"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	releaseLock, err := holdObservationJournal(file)
	if err != nil {
		file.Close()
		t.Fatal("actual journal lock", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if err := releaseLock(); err != nil {
			t.Error("actual journal unlock", err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}

func TestNativeStopWindowsProcessesExactMissionAndSibling(t *testing.T) {
	svc, a, skills, _, _ := task43CoreApprovedFixtureWithProof(t, "git diff --check")
	_, svc.Home = task43CoreHome(t)
	svc.NativeExecutorExecutable = nativeStopWindowsImage(t)
	t.Setenv("BATUTA_SKILLS", skills)
	adapter := filepath.Join(skills, "adapters", "codex.md")
	data, err := os.ReadFile(adapter)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(data), "-test.run=^TestTask43WindowsNativeCoreProviderFixture$", "-test.run=^$")
	body = strings.ReplaceAll(body, "task43-core-provider", "native-stop-provider")
	if err := os.Remove(adapter); err != nil {
		t.Fatal(err)
	}
	task43CoreWriteFile(t, adapter, body)
	account := domain.Account{ID: "acct_native_stop", Adapter: "codex", Connected: true, SessionCap: 2}
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	authenticateFixtureAccount(t, svc, account, "native-stop-synthetic")
	mission, err := svc.Store.GetMission(a.Identity.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	mission.SquadSnapshot = domain.Squad{ID: "stop-squad", Members: []domain.Member{{ID: "member", CLI: "codex", Model: "qa-native", AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}}
	mission.SquadID = mission.SquadSnapshot.ID
	if err := svc.Store.PutMission(mission); err != nil {
		t.Fatal(err)
	}
	b := nativeStopCloneMission(t, svc, a)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() {
		drain, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := svc.ShutdownNative(drain); err != nil {
			t.Error(err)
		}
	})
	foreign := exec.Command(os.Args[0], "--task43-core-hold")
	foreign.Env, _ = task43CoreHome(t)
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
	var born task43CoreBirth
	var birthErr error
	if err := foreign.Process.WithHandle(func(h uintptr) { born, birthErr = task43CoreBirthOf(windows.Handle(h), uint32(foreign.Process.Pid)) }); err != nil || birthErr != nil {
		t.Fatal(err, birthErr)
	}
	unrelated, err := task43CoreWitness(born)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(unrelated)
	first := nativeStopStart(t, ctx, svc, a)
	target := nativeStopWindowsTree(t, ctx, a)
	second := nativeStopStart(t, ctx, svc, b)
	sibling := nativeStopWindowsTree(t, ctx, b)
	nativeStopWindowsHandles(t, append(append(target, sibling...), unrelated), true)
	if first.RunID == second.RunID {
		t.Fatal("distinct missions share invocation")
	}
	unlock := nativeStopWindowsJournalLock(t, a, first.Delivery)
	caller, disconnect := context.WithCancel(ctx)
	req := nativeStopReference(a, first)
	pending, err := svc.StopExecution(caller, req)
	disconnect()
	if err != nil || pending.Effect != domain.ExecutionStopPending || pending.Finalized || pending.Durable {
		t.Fatal("public Stop acceptance", pending, err)
	}
	nativeStopAwait(t, ctx, "original Windows target retirement", func() bool {
		for _, h := range target[:2] {
			status, err := windows.WaitForSingleObject(h, 0)
			if err != nil {
				t.Fatal(err)
			}
			if status == uint32(windows.WAIT_TIMEOUT) {
				return false
			}
			if status != windows.WAIT_OBJECT_0 {
				t.Fatal("unexpected original handle state", status)
			}
		}
		return true
	})
	nativeStopWindowsHandles(t, target[2:], true)
	held, err := svc.StopExecution(ctx, req)
	if err != nil || held.Effect != domain.ExecutionStopPending || held.Finalized || held.Durable {
		t.Fatal("held actual Windows journal unwind", held, err)
	}
	nativeStopWindowsHandles(t, append(sibling, unrelated), true)
	unlock()
	nativeStopCanceledFinal(t, ctx, svc, req)
	nativeStopWindowsHandles(t, target, false)
	nativeStopWindowsHandles(t, append(sibling, unrelated), true)
	// The original sibling event is opened only after its birth witness. Signaling
	// it lets actual Core implement, integrate, and run the selected review CLI.
	name, err := windows.UTF16PtrFromString(nativeStopWindowsEvent(filepath.Join(b.Identity.Binding.CanonicalRoot, ".git")))
	if err != nil {
		t.Fatal(err)
	}
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetEvent(event); err != nil {
		windows.CloseHandle(event)
		t.Fatal(err)
	}
	if err := windows.CloseHandle(event); err != nil {
		t.Fatal(err)
	}
	nativeStopReviewed(t, ctx, svc, b, second)
	nativeStopWindowsHandles(t, sibling, false)
	nativeStopWindowsHandles(t, []windows.Handle{unrelated}, true)
	repeated, err := svc.StopExecution(ctx, req)
	if err != nil || !repeated.Finalized || repeated.ExecutionStopRequest != req {
		t.Fatal("exact historical result", repeated, err)
	}
	if err := os.Remove(filepath.Join(a.Identity.Binding.CanonicalRoot, ".git", "native-stop-birth.json")); err != nil {
		t.Fatal(err)
	}
	resumed, err := svc.ResumeExecution(ctx, domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: first.Delivery, OpenedDigest: first.OpenedDigest})
	if err != nil || !domain.ValidExecutionRunID(resumed.RunID) || resumed.RunID == first.RunID {
		t.Fatal("Windows fresh resume authority", resumed, err)
	}
	replacement := nativeStopWindowsTree(t, ctx, a)
	historical, err := svc.StopExecution(ctx, req)
	if err != nil || !historical.Finalized || historical.ExecutionStopRequest != req || (historical.State != nil && (historical.State.Active || historical.State.RunID != "")) {
		t.Fatal("old Windows Stop selected replacement", historical, err)
	}
	nativeStopWindowsHandles(t, replacement, true)
	state, err := svc.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
	if err != nil || !state.Active || state.RunID != resumed.RunID {
		t.Fatal("Windows replacement identity", state, err)
	}
	nativeStopCanceledFinal(t, ctx, svc, nativeStopReference(a, resumed))
	nativeStopWindowsHandles(t, replacement, false)
	nativeStopWindowsHandles(t, []windows.Handle{unrelated}, true)
	t.Log("actual Windows Core55 public Stop: retained original process handles, two workspaces, sibling SHIP; provider reported membership in a Job (specific Job not queried)")
}

func TestNativeStopWindowsJournalLockActuallyBlocksSnapshot(t *testing.T) {
	root := t.TempDir()
	js, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	const delivery = "stop-lock-control"
	if _, err := js.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: json.RawMessage(`{"slug":"lock-control"}`)}); err != nil {
		t.Fatal(err)
	}
	a := domain.PlanApproval{Identity: domain.PlanIdentity{Binding: domain.MissionBinding{CanonicalRoot: root}}}
	unlock := nativeStopWindowsJournalLock(t, a, delivery)
	file, err := os.Open(js.Path(delivery))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	snapshot, err := journal.ReadSnapshot(ctx, file)
	if !errors.Is(err, context.DeadlineExceeded) || snapshot.Complete() || len(snapshot.Records()) != 0 {
		t.Fatalf("fixture must block the real journal lock rather than deny journal reads: err=%v snapshot=%+v", err, snapshot)
	}
	unlock()
	snapshot, err = journal.ReadSnapshot(context.Background(), file)
	if err != nil || !snapshot.Complete() || len(snapshot.Records()) != 1 {
		t.Fatal("unlocked real journal unreadable", snapshot, err)
	}
}

func TestNativeStopWindowsCancellationOnlyRoundTrip(t *testing.T) {
	opaque := errors.New(context.Canceled.Error())
	var absent error
	for _, test := range []struct {
		name  string
		cause error
		only  bool
	}{
		{"pure", context.Canceled, true},
		{"wrapped", fmt.Errorf("native cancel: %w", context.Canceled), true},
		{"joined", errors.Join(context.Canceled, context.Canceled), true},
		{"same_text_independent", errors.Join(context.Canceled, opaque), false},
		{"nil_wrapper", errors.Join(context.Canceled, fmt.Errorf("resource failed: %w", absent)), false},
		{"independent_cleanup", errors.Join(context.Canceled, errors.New("resource failed")), false},
		{"kernel", errors.Join(context.Canceled, windows.ERROR_ACCESS_DENIED), false},
		{"uncertain", errors.Join(context.Canceled, domain.ErrExecutionUncertain), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := test.cause
			for hop := 0; hop < 2; hop++ {
				var wire bytes.Buffer
				stream := nativeCoreStream{reader: &wire, writer: &wire}
				if err := stream.send(nativeCoreFrame{Type: "done", Done: &nativeCoreDone{State: loop.StateCanceled, Error: nativeCoreEncodeError(cause)}}); err != nil {
					t.Fatal(err)
				}
				frame, err := stream.receive()
				if err != nil || frame.Done == nil || frame.Done.Error == nil {
					t.Fatal("actual framed cancellation result", frame, err)
				}
				decoded := frame.Done.Error.cause()
				if onlyNativeCancellation(decoded) != test.only || !errors.Is(decoded, context.Canceled) {
					t.Fatalf("hop%d: cancellation-only classification lost: want=%v err=%v", hop, test.only, decoded)
				}
				if frame.Done.Error.Message != cause.Error() || test.only && decoded.Error() != cause.Error() {
					t.Fatal("lost cancellation message", frame.Done.Error, decoded)
				}
				if test.name == "kernel" && !errors.Is(decoded, windows.ERROR_ACCESS_DENIED) {
					t.Fatal("lost actual kernel cause", decoded)
				}
				if test.name == "uncertain" && !errors.Is(decoded, domain.ErrExecutionUncertain) {
					t.Fatal("lost uncertainty cause", decoded)
				}
				cause = decoded
			}
		})
	}
}

func TestNativeStopWindowsContradictoryCancellationMetadataIsNotPure(t *testing.T) {
	for _, test := range []struct {
		name    string
		encoded nativeCoreError
	}{
		{"missing_cancel", nativeCoreError{Message: "opaque", OnlyCanceled: true}},
		{"deadline", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, Deadline: true}},
		{"cleanup", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, Cleanup: true}},
		{"uncertain", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, Uncertain: true}},
		{"invalid", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, Invalid: true}},
		{"binding", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, Binding: true}},
		{"review_cleanup", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, ReviewCleanup: true}},
		{"kernel", nativeCoreError{Message: "opaque", OnlyCanceled: true, Canceled: true, Kernel: []uint32{uint32(windows.ERROR_ACCESS_DENIED)}}},
		{"legacy_canceled", nativeCoreError{Message: context.Canceled.Error(), Canceled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if onlyNativeCancellation(test.encoded.cause()) {
				t.Fatal("contradictory or legacy opaque metadata hid a cause", test.encoded)
			}
		})
	}
}
