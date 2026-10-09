package session

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTask43WindowsTerminationQueryFailureKeepsCause(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	env, err := NativeEnvironment(runner.Environment, nil)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := newWindowsJob()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !owned.retired {
			if err := owned.retire(nil); err != nil && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Error(err)
			}
		}
	}()
	command := &exec.Cmd{Env: env}
	if err := owned.createAnchor(command); err != nil {
		t.Fatal(err)
	}
	original := owned.job
	var before windowsJobAccounting
	if err := queryWindowsJob(original, windows.JobObjectBasicAccountingInformation, unsafe.Pointer(&before), uint32(unsafe.Sizeof(before))); err != nil || before.TotalProcesses != 1 || before.ActiveProcesses != 1 {
		t.Fatalf("original private Job has no actual suspended member: %+v %v", before, err)
	}
	const jobTerminateOnly = 0x0008
	var limited windows.Handle
	current := windows.CurrentProcess()
	if err := windows.DuplicateHandle(current, original, current, &limited, jobTerminateOnly, false, 0); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if owned.job == limited {
			owned.job = original
		}
		if err := windows.CloseHandle(limited); err != nil {
			t.Error(err)
		}
	}()
	owned.mu.Lock()
	owned.job = limited
	firstErr := owned.signalLocked()
	owned.job = original
	owned.mu.Unlock()
	if !errors.Is(firstErr, windows.ERROR_ACCESS_DENIED) || !owned.signaled {
		t.Fatalf("actual query denial or original Job termination missing: %v signaled=%t", firstErr, owned.signaled)
	}
	status, err := windows.WaitForSingleObject(owned.anchor.Process, 2000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("original suspended member was not terminated by its Job: wait=%d err=%v", status, err)
	}
	retireErr := owned.retire(nil)
	if !errors.Is(retireErr, windows.ERROR_ACCESS_DENIED) || strings.Contains(retireErr.Error(), "gained unobserved processes") {
		t.Fatalf("query failure lost its cause or invented a birth: %v", retireErr)
	}
	if !owned.retired || len(owned.terminationWaiters) != 0 || owned.job != 0 || owned.root != 0 || owned.anchor.Process != 0 || owned.anchor.Thread != 0 || owned.port != 0 {
		t.Fatal("failed accounting retained original creation handles", owned)
	}
	t.Logf("actual original total=%d firstQuery=%v terminated=%t final=%v", before.TotalProcesses, firstErr, owned.signaled, retireErr)
}
