package session

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func task43AdmissionJob(t *testing.T) (*ownedProcess, *[]windows.ProcessInformation) {
	t.Helper()
	owned, err := newWindowsJob()
	if err != nil {
		t.Fatal(err)
	}
	var births []windows.ProcessInformation
	t.Cleanup(func() {
		if owned.job != 0 {
			if err := windows.TerminateJobObject(owned.job, 1); err != nil {
				t.Error("explicit private Job cleanup", err)
			}
		}
		for _, process := range append(births, owned.anchor) {
			if process.Process == 0 {
				continue
			}
			status, err := windows.WaitForSingleObject(process.Process, 2000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Error("private Job cleanup did not terminate original process handle", status, err)
				// A failed Job cleanup must not leak even a suspended process.
				if err := windows.TerminateProcess(process.Process, 1); err != nil {
					t.Error("original creation handle cleanup", err)
				}
				if status, err := windows.WaitForSingleObject(process.Process, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Error("original creation handle termination wait", status, err)
				}
			}
			if status, err := windows.WaitForSingleObject(process.Thread, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Error("original thread termination wait", status, err)
			}
		}
		for _, process := range births {
			if err := windows.CloseHandle(process.Thread); err != nil {
				t.Error(err)
			}
			if err := windows.CloseHandle(process.Process); err != nil {
				t.Error(err)
			}
		}
		if err := owned.closeHandles(); err != nil {
			t.Error(err)
		}
	})
	if err := owned.createAnchor(&exec.Cmd{Env: os.Environ()}); err != nil {
		t.Fatal("create original suspended Job member", err)
	}
	return owned, &births
}

func task43AdmissionBirth(t *testing.T, job windows.Handle, births *[]windows.ProcessInformation) error {
	t.Helper()
	argv := task43ProbeArgv(t, "hold")
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		t.Fatal(err)
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windowsJobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		t.Fatal(err)
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb = uint32(unsafe.Sizeof(info))
	var process windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.DETACHED_PROCESS | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcess(app, command, nil, nil, false, flags, nil, nil, &info.StartupInfo, &process)
	if err == nil {
		// Retain both original handles before inspecting an unexpected birth.
		*births = append(*births, process)
	}
	return err
}

func task43AdmissionAccounting(t *testing.T, job windows.Handle) windowsJobAccounting {
	t.Helper()
	var accounting windowsJobAccounting
	if err := queryWindowsJob(job, windows.JobObjectBasicAccountingInformation, unsafe.Pointer(&accounting), uint32(unsafe.Sizeof(accounting))); err != nil {
		t.Fatal(err)
	}
	return accounting
}

func task43AdmissionMemberAlive(t *testing.T, job windows.Handle, process windows.ProcessInformation) {
	t.Helper()
	var member int32
	ok, _, err := windowsProcessInJob.Call(uintptr(process.Process), uintptr(job), uintptr(unsafe.Pointer(&member)))
	if ok == 0 || member != 1 {
		t.Fatal("original process handle is not in its private Job", member, err)
	}
	for _, handle := range []windows.Handle{process.Process, process.Thread} {
		if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("original suspended member did not remain alive", handle, status, err)
		}
	}
}

func TestTask43WindowsStoppedJobRejectsNativeBirth(t *testing.T) {
	owned, births := task43AdmissionJob(t)
	originalJob := owned.job
	task43AdmissionMemberAlive(t, originalJob, owned.anchor)
	before := task43AdmissionAccounting(t, originalJob)
	if before.TotalProcesses != 1 || before.ActiveProcesses != 1 {
		t.Fatalf("missing original suspended Job member: %+v", before)
	}
	if err := owned.stop(); err != nil {
		t.Fatal("stop original private Job", err)
	}
	if owned.job != originalJob || !owned.signaled || !owned.terminationObserved || !owned.terminationTotalObserved || owned.terminationTotal != before.TotalProcesses || len(owned.terminationWaiters) != 1 {
		t.Fatal("stop lost original Job or strict member observation", owned)
	}
	if status, err := windows.WaitForSingleObject(owned.anchor.Process, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("stop did not terminate the original suspended member", status, err)
	}
	if err := owned.drainJob(); err != nil {
		t.Fatal("original Job did not drain before admission attempt", err)
	}
	err := task43AdmissionBirth(t, originalJob, births)
	if err == nil {
		t.Errorf("stopped private Job admitted a new suspended process through PROC_THREAD_ATTRIBUTE_JOB_LIST; user_code_resumed=false")
		task43AdmissionMemberAlive(t, originalJob, (*births)[0])
		after := task43AdmissionAccounting(t, originalJob)
		t.Logf("unexpected native birth: total=%d->%d active=%d process=%#x thread=%#x", before.TotalProcesses, after.TotalProcesses, after.ActiveProcesses, (*births)[0].Process, (*births)[0].Thread)
		if after.TotalProcesses != before.TotalProcesses+1 || after.ActiveProcesses != 1 {
			t.Error("unexpected birth missing from actual Job accounting", after)
		}
		return
	}
	if !errors.Is(err, windows.ERROR_NOT_ENOUGH_QUOTA) {
		t.Fatal("native admission failed for a reason other than the Job admission limit", err)
	}
	after := task43AdmissionAccounting(t, originalJob)
	if after.TotalProcesses != before.TotalProcesses || after.ActiveProcesses != 0 || owned.terminationTotal != before.TotalProcesses {
		t.Fatal("rejected birth changed strict Job accounting", before, after, owned.terminationTotal)
	}
	if err := owned.retire(nil); err != nil {
		t.Fatal("rejected birth made known termination unresolved", err)
	}
	t.Logf("native admission refused: reason=%v total=%d->%d user_code_resumed=false", err, before.TotalProcesses, after.TotalProcesses)
}

func TestTask43WindowsZeroAdmissionPreservesHeldMember(t *testing.T) {
	owned, births := task43AdmissionJob(t)
	var original windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := queryWindowsJob(owned.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&original), uint32(unsafe.Sizeof(original))); err != nil {
		t.Fatal(err)
	}
	original.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS
	original.BasicLimitInformation.PriorityClass = windows.NORMAL_PRIORITY_CLASS
	if err := setWindowsJob(owned.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&original), uint32(unsafe.Sizeof(original))); err != nil {
		t.Fatal("set ordinary priority limit alongside KILL_ON_JOB_CLOSE", err)
	}
	if err := queryWindowsJob(owned.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&original), uint32(unsafe.Sizeof(original))); err != nil {
		t.Fatal(err)
	}
	if original.BasicLimitInformation.LimitFlags&(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE|windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS) != windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE|windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS || original.BasicLimitInformation.PriorityClass != windows.NORMAL_PRIORITY_CLASS {
		t.Fatal("original Job limits missing", original.BasicLimitInformation)
	}
	task43AdmissionMemberAlive(t, owned.job, owned.anchor)
	before := task43AdmissionAccounting(t, owned.job)
	if before.TotalProcesses != 1 || before.ActiveProcesses != 1 {
		t.Fatal("missing original suspended member before zero limit", before)
	}
	zero := original
	zero.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	zero.BasicLimitInformation.ActiveProcessLimit = 0
	if err := setWindowsJob(owned.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&zero), uint32(unsafe.Sizeof(zero))); err != nil {
		t.Fatal("kernel rejected zero active-process admission limit", err)
	}
	var observed windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := queryWindowsJob(owned.job, windows.JobObjectExtendedLimitInformation, unsafe.Pointer(&observed), uint32(unsafe.Sizeof(observed))); err != nil {
		t.Fatal(err)
	}
	if observed.BasicLimitInformation != zero.BasicLimitInformation || observed.ProcessMemoryLimit != original.ProcessMemoryLimit || observed.JobMemoryLimit != original.JobMemoryLimit {
		t.Fatal("zero admission limit changed other original Job limits", original, zero, observed)
	}
	task43AdmissionMemberAlive(t, owned.job, owned.anchor)
	afterLimit := task43AdmissionAccounting(t, owned.job)
	if afterLimit.TotalProcesses != before.TotalProcesses || afterLimit.ActiveProcesses != before.ActiveProcesses || afterLimit.TotalTerminatedProcesses != before.TotalTerminatedProcesses {
		t.Fatal("zero admission limit terminated or replaced an existing member", before, afterLimit)
	}
	err := task43AdmissionBirth(t, owned.job, births)
	if !errors.Is(err, windows.ERROR_NOT_ENOUGH_QUOTA) {
		t.Fatal("zero admission limit did not refuse a second native birth with quota error; user_code_resumed=false", err)
	}
	task43AdmissionMemberAlive(t, owned.job, owned.anchor)
	afterBirth := task43AdmissionAccounting(t, owned.job)
	if afterBirth.TotalProcesses != before.TotalProcesses || afterBirth.ActiveProcesses != before.ActiveProcesses || afterBirth.TotalTerminatedProcesses != before.TotalTerminatedProcesses {
		t.Fatal("refused native birth changed original membership accounting", before, afterBirth)
	}
	t.Logf("zero admission preserved held member: flags=%#x limit=%d total=%d active=%d reason=%v user_code_resumed=false", observed.BasicLimitInformation.LimitFlags, observed.BasicLimitInformation.ActiveProcessLimit, afterBirth.TotalProcesses, afterBirth.ActiveProcesses, err)
}
