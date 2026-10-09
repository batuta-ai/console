package session

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTask43WindowsTerminationNewBirthIsUnresolved(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	env, err := NativeEnvironment(runner.Environment, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv := task43ProbeArgv(t, "hold")
	root := exec.Command(argv[0], argv[1:]...)
	root.Env = env
	owned, err := startOwnedCommand(root)
	if err != nil {
		t.Fatal(err)
	}
	retired := false
	t.Cleanup(func() {
		if !retired {
			if err := owned.retire(nil); err != nil {
				t.Error(err)
			}
		}
		var failure *exec.ExitError
		if err := root.Wait(); err != nil && !errors.As(err, &failure) {
			t.Error(err)
		}
	})
	owned.mu.Lock()
	err = owned.observeTermination()
	owned.terminationObserved = true
	before := owned.terminationTotal
	owned.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	later := exec.Command(argv[0], argv[1:]...)
	later.Env = env
	later.SysProcAttr = &syscall.SysProcAttr{ParentProcess: syscall.Handle(owned.anchor.Process), CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	if err := later.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !retired {
			if err := owned.retire(nil); err != nil {
				t.Error(err)
			}
			retired = true
		}
		var failure *exec.ExitError
		if err := later.Wait(); err != nil && !errors.As(err, &failure) {
			t.Error(err)
		}
	})
	var member int32
	var observeErr error
	if err := later.Process.WithHandle(func(handle uintptr) {
		ok, _, callErr := windowsProcessInJob.Call(handle, uintptr(owned.job), uintptr(unsafe.Pointer(&member)))
		if ok == 0 {
			observeErr = callErr
		}
	}); err != nil || observeErr != nil || member != 1 {
		t.Fatal("new native process did not inherit the original private Job", member, err, observeErr)
	}
	var after windowsJobAccounting
	if err := queryWindowsJob(owned.job, windows.JobObjectBasicAccountingInformation, unsafe.Pointer(&after), uint32(unsafe.Sizeof(after))); err != nil || after.TotalProcesses <= before {
		t.Fatal("actual new Job birth was not observed by kernel accounting", before, after, err)
	}
	err = owned.retire(nil)
	retired = true
	if err == nil || !strings.Contains(err.Error(), "owned Windows job gained unobserved processes before termination") {
		t.Fatal("new birth during termination was reported as a complete known tree", err)
	}
	if !owned.retired || len(owned.terminationWaiters) != 0 || owned.job != 0 || owned.root != 0 || owned.anchor.Process != 0 || owned.anchor.Thread != 0 || owned.port != 0 {
		t.Fatal("uncertain termination retained original Job handles", owned)
	}
	t.Logf("actual private Job before=%d after=%d membership=%d outcome=%v", before, after.TotalProcesses, member, err)
}
