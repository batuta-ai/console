package session

import (
	"errors"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTask43WindowsAdmissionSetFailureKeepsCause(t *testing.T) {
	owned, _ := task43AdmissionJob(t)
	originalJob := owned.job
	task43AdmissionMemberAlive(t, originalJob, owned.anchor)
	before := task43AdmissionAccounting(t, originalJob)
	if before.TotalProcesses != 1 || before.ActiveProcesses != 1 {
		t.Fatal("missing original suspended Job member", before)
	}
	current := windows.CurrentProcess()
	if err := windows.DuplicateHandle(current, owned.anchor.Process, current, &owned.root, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	const jobQueryAndTerminate = 0x0004 | 0x0008
	var limited windows.Handle
	if err := windows.DuplicateHandle(current, originalJob, current, &limited, jobQueryAndTerminate, false, 0); err != nil {
		t.Fatal(err)
	}
	// Keep full authority over the same Job for cleanup even if stop fails.
	t.Cleanup(func() {
		if err := windows.TerminateJobObject(originalJob, 1); err != nil {
			t.Error("original private Job cleanup", err)
		}
		if err := windows.CloseHandle(originalJob); err != nil {
			t.Error(err)
		}
	})
	owned.job = limited
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := queryWindowsJob(limited, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal("restricted original Job must permit admission-limit query", err)
	}
	if got := task43AdmissionAccounting(t, limited); got != before {
		t.Fatal("restricted duplicate changed original Job accounting", before, got)
	}
	stopErr := owned.stop()
	if !errors.Is(stopErr, windows.ERROR_ACCESS_DENIED) || !strings.Contains(stopErr.Error(), "seal owned Windows job admission:") {
		t.Fatalf("actual admission SET denial missing: %v", stopErr)
	}
	if !errors.Is(owned.err, windows.ERROR_ACCESS_DENIED) || !owned.signaled || owned.job != limited {
		t.Fatal("SET failure lost its cause or prevented original Job termination", owned)
	}
	if !owned.terminationObserved || !owned.terminationTotalObserved || owned.terminationTotal != before.TotalProcesses || len(owned.terminationWaiters) != 1 {
		t.Fatal("SET failure prevented strict original member observation", owned)
	}
	for _, handle := range []windows.Handle{owned.root, owned.anchor.Process, owned.anchor.Thread} {
		if status, err := windows.WaitForSingleObject(handle, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal("SET failure prevented original member termination", handle, status, err)
		}
	}
	if err := owned.drainJob(); err != nil {
		t.Fatal("restricted original Job did not drain", err)
	}
	after := task43AdmissionAccounting(t, originalJob)
	if after.TotalProcesses != before.TotalProcesses || after.ActiveProcesses != 0 {
		t.Fatal("SET failure changed original membership or left a live member", before, after)
	}
	retireErr := owned.retire(nil)
	if !errors.Is(retireErr, windows.ERROR_ACCESS_DENIED) || !strings.Contains(retireErr.Error(), "seal owned Windows job admission:") || strings.Contains(retireErr.Error(), "gained unobserved processes") {
		t.Fatalf("retire lost the SET denial or invented a birth: %v", retireErr)
	}
	if !owned.retired || len(owned.terminationWaiters) != 0 || owned.job != 0 || owned.root != 0 || owned.anchor.Process != 0 || owned.anchor.Thread != 0 || owned.port != 0 {
		t.Fatal("failed admission SET retained owned handles", owned)
	}
	if err := owned.retire(nil); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("repeated retire lost the admission SET denial", err)
	}
	t.Logf("original Job SET denied: stop=%v retained=%v total=%d->%d active=%d signaled=%t user_code_resumed=false", stopErr, retireErr, before.TotalProcesses, after.TotalProcesses, after.ActiveProcesses, owned.signaled)
}
