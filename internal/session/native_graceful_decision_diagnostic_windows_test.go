package session

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TEMPORARY NONSHIPPING: these tests qualify observation, never a cleanup fix.
func TestGraceDecisionDiagnosticReadOnly(t *testing.T) {
	owned, _ := task43AdmissionJob(t)
	p := &ownedProcess{job: owned.job, root: owned.anchor.Process, pid: owned.anchor.ProcessId, native: true}
	accounting := task43AdmissionAccounting(t, p.job)
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := queryWindowsJob(p.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&limits), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	// Deliberately different from live accounting: the trace must retain its input.
	original := accounting
	original.ActiveProcesses = 73
	p.mu.Lock()
	r := collectGraceDecisionDiagnostic(p, original, windows.ERROR_ACCESS_DENIED)
	p.mu.Unlock()
	if r.Accounting != original || r.AccountingError == "" || r.Owned.Native != p.native || r.Owned.Exited != p.exited || r.Owned.Forced != p.forced {
		t.Fatal("original decision inputs lost", r)
	}
	if r.Job != p.job || r.Root.Handle != p.root || r.ListError != "" || r.Incomplete || r.Listed != 1 || len(r.Members) != 1 {
		t.Fatal("actual Job/root provenance lost", r)
	}
	for _, got := range []graceDecisionProcess{r.Root, r.Members[0]} {
		if got.PID == nil || *got.PID != p.pid || got.Birth == nil || got.InJob == nil || !*got.InJob || got.Wait == nil || *got.Wait != uint32(windows.WAIT_TIMEOUT) || got.ExitCode == nil || *got.ExitCode != 259 || got.Image == nil || !strings.HasSuffix(strings.ToLower(*got.Image), "\\rundll32.exe") || len(got.Errors) != 0 {
			t.Fatal("missing real process identity/status/image", got)
		}
		last := int64(0)
		for _, step := range got.Steps {
			if step.StartNS < last || step.EndNS < step.StartNS {
				t.Fatal("observation order lost", got.Steps)
			}
			last = step.EndNS
		}
	}
	member := r.Members[0]
	if member.HandleFlags == nil || *member.HandleFlags&windows.HANDLE_FLAG_INHERIT != 0 || member.CloseError != "" {
		t.Fatal("member handle inheritance/closure absent", member)
	}
	if _, err := windows.WaitForSingleObject(member.Handle, 0); err != windows.ERROR_INVALID_HANDLE {
		t.Fatal("diagnostic member handle leaked", err)
	}
	task43AdmissionMemberAlive(t, p.job, owned.anchor)
	var after windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := queryWindowsJob(p.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&after), uint32(unsafe.Sizeof(after))); err != nil || !reflect.DeepEqual(after, limits) {
		t.Fatal("observation changed Job limits", after, limits, err)
	}
	if p.forced || p.signaled || p.retired || p.err != nil || p.terminationObserved || len(p.terminationWaiters) != 0 || p.root != owned.anchor.Process || p.job != owned.job {
		t.Fatal("observation mutated owned state", p)
	}
	assertGraceDecisionJSON(t, r)
}

func TestGraceDecisionDiagnosticUnknown(t *testing.T) {
	p := &ownedProcess{native: true, exited: true, exitCode: 19}
	p.mu.Lock()
	r := collectGraceDecisionDiagnostic(p, windowsJobAccounting{}, windows.ERROR_INVALID_HANDLE)
	p.mu.Unlock()
	if r.AccountingError == "" || r.ListError == "" || !r.Incomplete || len(r.Members) != 0 || r.Root.PID != nil || r.Root.Birth != nil || r.Root.InJob != nil || r.Root.Wait != nil || r.Root.ExitCode != nil || r.Root.Image != nil || len(r.Root.Errors) < 6 {
		t.Fatal("failed queries became successful guesses", r)
	}
	if p.err != nil || p.forced || p.signaled || p.retired || r.Owned.ExitCode == nil || *r.Owned.ExitCode != 19 {
		t.Fatal("diagnostic errors contaminated owned state", p, r)
	}
	assertGraceDecisionJSON(t, r)
}

func TestGraceDecisionDiagnosticMissingJobWithOriginalRoot(t *testing.T) {
	owned, _ := task43AdmissionJob(t)
	p := &ownedProcess{root: owned.anchor.Process, pid: owned.anchor.ProcessId, native: true}
	original := windowsJobAccounting{ActiveProcesses: 73}
	p.mu.Lock()
	r := collectGraceDecisionDiagnostic(p, original, windows.ERROR_INVALID_HANDLE)
	p.mu.Unlock()
	if r.Job != 0 || r.ListError == "" || !strings.Contains(r.ListError, "missing owned Job") || !r.Incomplete || r.Listed != 0 || r.RawList != (graceDecisionList{}) || len(r.Members) != 0 || !strings.Contains(r.ListStep.API, "skipped") {
		t.Fatal("missing owned Job queried ambient Job or became an empty known Job", r)
	}
	root := r.Root
	if root.Handle != p.root || root.PID == nil || *root.PID != p.pid || root.Birth == nil || root.Wait == nil || *root.Wait != uint32(windows.WAIT_TIMEOUT) || root.ExitCode == nil || *root.ExitCode != 259 || root.Image == nil || !strings.HasSuffix(strings.ToLower(*root.Image), "\\rundll32.exe") || root.HandleFlags == nil {
		t.Fatal("missing Job discarded actual original-root identity/status", root)
	}
	if root.InJob != nil || len(root.Errors) != 1 || !strings.Contains(root.Errors["IsProcessInJob"], "missing owned Job") {
		t.Fatal("any-Job membership substituted for unknown owned-Job membership", root)
	}
	for _, step := range root.Steps {
		if step.API == "IsProcessInJob" {
			t.Fatal("missing owned Job must skip the membership API", root)
		}
	}
	if r.Accounting != original || r.AccountingError != graceDecisionError(windows.ERROR_INVALID_HANDLE) || p.job != 0 || p.root != owned.anchor.Process || p.pid != owned.anchor.ProcessId || !p.native || p.exited || p.forced || p.signaled || p.retired || p.err != nil || p.terminationObserved || p.terminationTotalObserved || p.terminationTotal != 0 || len(p.terminationWaiters) != 0 {
		t.Fatal("missing-Job diagnostic changed original inputs or owned state", p, r)
	}
	task43AdmissionMemberAlive(t, owned.job, owned.anchor)
	assertGraceDecisionJSON(t, r)
}

func TestGraceDecisionDiagnosticExitedOriginalRoot(t *testing.T) {
	owned, _ := task43AdmissionJob(t)
	root := owned.anchor.Process
	if err := windows.TerminateProcess(root, 23); err != nil {
		t.Fatal(err)
	}
	if status, err := windows.WaitForSingleObject(root, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("fixture did not exit", status, err)
	}
	p := &ownedProcess{job: owned.job, root: root, pid: owned.anchor.ProcessId, native: true, exited: true, exitCode: 23}
	p.mu.Lock()
	r := collectGraceDecisionDiagnostic(p, task43AdmissionAccounting(t, p.job), nil)
	p.mu.Unlock()
	if r.Root.PID == nil || *r.Root.PID != p.pid || r.Root.Birth == nil || r.Root.Wait == nil || *r.Root.Wait != windows.WAIT_OBJECT_0 || r.Root.ExitCode == nil || *r.Root.ExitCode != 23 || r.Owned.ExitCode == nil || *r.Owned.ExitCode != 23 {
		t.Fatal("original exited process object lost", r)
	}
	// Image availability after exit is OS-dependent; failure must be explicit.
	if r.Root.Image == nil && r.Root.Errors["QueryFullProcessImageName"] == "" {
		t.Fatal("missing image has no query error", r.Root)
	}
	if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != windows.WAIT_OBJECT_0 || p.forced || p.err != nil {
		t.Fatal("observer closed original root or altered owned outcome", status, err, p)
	}
	assertGraceDecisionJSON(t, r)
}

func TestGraceDecisionDiagnosticBoundedMembers(t *testing.T) {
	owned, births := task43AdmissionJob(t)
	for i := 0; i < 32; i++ {
		if err := task43AdmissionBirth(t, owned.job, births); err != nil {
			t.Fatal(err)
		}
	}
	p := &ownedProcess{job: owned.job, root: owned.anchor.Process}
	r := collectGraceDecisionDiagnostic(p, task43AdmissionAccounting(t, owned.job), nil)
	if !r.Incomplete || len(r.Members) > 32 || len(r.RawList.IDs) != 32 {
		t.Fatal("Job list bound not explicit", r)
	}
	if r.ListError != "" && len(r.Members) != 0 {
		t.Fatal("failed list query promoted partial PIDs to members", r)
	}
	task43AdmissionMemberAlive(t, owned.job, owned.anchor)
	for _, process := range *births {
		task43AdmissionMemberAlive(t, owned.job, process)
	}
	assertGraceDecisionJSON(t, r)
}

func TestGraceDecisionDiagnosticOutputFailure(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "trace")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r := collectGraceDecisionDiagnostic(&ownedProcess{}, windowsJobAccounting{}, nil)
	if err := writeGraceDecisionDiagnostic(f, r); err == nil {
		t.Fatal("real closed-stream error discarded")
	}
	if r.OutputError != "" {
		t.Fatal("output mutated collected record")
	}
	capture, err := os.CreateTemp(t.TempDir(), "delivery-error")
	if err != nil {
		t.Fatal(err)
	}
	stderr, stdout := os.Stderr, os.Stdout
	defer func() { os.Stderr, os.Stdout = stderr, stdout }()
	os.Stderr, os.Stdout = f, capture
	p := &ownedProcess{native: true, exited: true}
	p.mu.Lock()
	traceGraceDecisionDiagnostic(p, windowsJobAccounting{}, windows.ERROR_INVALID_HANDLE)
	p.mu.Unlock()
	os.Stderr, os.Stdout = stderr, stdout
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatal("diagnostic delivery failure contaminated stdout", string(data))
	}
	if p.err != nil || p.forced || p.signaled || p.retired {
		t.Fatal("output error contaminated owned outcome", p)
	}
}

func assertGraceDecisionJSON(t *testing.T, r graceDecisionRecord) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "trace")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGraceDecisionDiagnostic(f, r); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte("GRACE-DECISION ")
	if len(data) > graceDecisionOutputLimit || !bytes.HasPrefix(data, prefix) || bytes.Count(data, []byte("\n")) != 1 || !json.Valid(bytes.TrimSuffix(data[len(prefix):], []byte("\n"))) || !bytes.Contains(data, []byte("NEVER-SHIP")) || !bytes.Contains(data, []byte("non-atomic")) {
		t.Fatal("trace envelope is not bounded, explicit JSON", string(data))
	}
}
