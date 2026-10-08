package session

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsJobDescendantDiagnosisFixture(t *testing.T) {
	if len(os.Args) < 4 || os.Args[3] != "task43-job-diagnosis" {
		return
	}
	fmt.Fprintln(os.Stdout, "before child command")
	mode := "hold"
	if len(os.Args) > 4 && os.Args[4] == "exiting-child" {
		mode = "nonzero"
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTask43WindowsProbeProcessFixture$", "--", "task43-probe-fixture", mode)
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
	if len(os.Args) > 4 && os.Args[4] == "null-child-io" {
		child.Stdout, child.Stderr = nil, nil
	}
	fmt.Fprintln(os.Stdout, "before child start")
	timer := time.AfterFunc(200*time.Millisecond, func() {
		buf := make([]byte, 24<<10)
		n := runtime.Stack(buf, true)
		_, _ = os.Stdout.Write(buf[:n])
	})
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	timer.Stop()
	fmt.Fprintln(os.Stdout, "after child start")
	var created, exited, kernel, user windows.Filetime
	var inspectErr error
	if err := child.Process.WithHandle(func(h uintptr) {
		inspectErr = windows.GetProcessTimes(windows.Handle(h), &created, &exited, &kernel, &user)
	}); err != nil || inspectErr != nil {
		t.Fatal(err, inspectErr)
	}
	fmt.Fprintln(os.Stdout, "after creation handle")
	os.Exit(0)
}

func TestTask43WindowsJobDescendantStageDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "")
}

func TestTask43WindowsJobHiddenConsoleDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "hidden-console")
}

func TestTask43WindowsJobRetiredAnchorDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "retired-anchor")
}

func TestTask43WindowsJobNullChildIODiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "null-child-io")
}

func TestTask43WindowsJobExitingChildDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "exiting-child")
}

func TestTask43WindowsJobLongCreationObservation(t *testing.T) {
	task43JobRootDiagnosis(t, true, "long-observation")
}

func task43ObserveThreadWaits(t *testing.T, pid uint32) {
	advapi := windows.NewLazySystemDLL("advapi32.dll")
	session, _, err := advapi.NewProc("OpenThreadWaitChainSession").Call(0, 0)
	if session == 0 {
		t.Log("WCT observation unavailable", err)
		return
	}
	defer advapi.NewProc("CloseThreadWaitChainSession").Call(session)
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		t.Log(err)
		return
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		nodes := [16]struct {
			Type   uint32
			Status uint32
			Data   [34]uint64
		}{}
		count := uint32(16)
		var cycle int32
		ok, _, callErr := advapi.NewProc("GetThreadWaitChain").Call(session, 0, 0, uintptr(entry.ThreadID), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&nodes[0])), uintptr(unsafe.Pointer(&cycle)))
		if ok == 0 {
			t.Log("WCT thread", entry.ThreadID, callErr)
			continue
		}
		for i := uint32(0); i < count && i < 16; i++ {
			typeID, status := nodes[i].Type, nodes[i].Status
			if typeID == 8 {
				data := (*[272]byte)(unsafe.Pointer(&nodes[i].Data))
				t.Logf("WCT sourceThread=%d node=%d type=%d status=%d process=%d thread=%d cycle=%d", entry.ThreadID, i, typeID, status, binary.LittleEndian.Uint32(data[:4]), binary.LittleEndian.Uint32(data[4:8]), cycle)
			} else {
				t.Logf("WCT sourceThread=%d node=%d type=%d status=%d cycle=%d", entry.ThreadID, i, typeID, status, cycle)
			}
		}
	}
}

func TestTask43WindowsJobDirectCreationDiagnosis(t *testing.T) {
	task43DirectJobDiagnosis(t, true, false)
}

func TestTask43WindowsJobWithoutCompletionPortDiagnosis(t *testing.T) {
	task43DirectJobDiagnosis(t, false, false)
}

func TestTask43WindowsJobSuspendedAssignmentDiagnosis(t *testing.T) {
	task43DirectJobDiagnosis(t, true, true)
}

func task43DirectJobDiagnosis(t *testing.T, completion, assignment bool) {
	home := task43SessionHome(t)
	env, privateHome, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(privateHome)
	var owned *ownedProcess
	if completion {
		owned, err = newWindowsJob()
	} else {
		owned = &ownedProcess{}
		owned.job, err = windows.CreateJobObject(nil, nil)
		if err == nil {
			limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
			limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
			_, err = windows.SetInformationJobObject(owned.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
		}
		if err == nil {
			owned.port, err = windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !owned.retired {
			_ = owned.retire(nil)
		}
	}()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var files [2]windows.Handle
	for i, f := range []*os.File{input, writer} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(f.Fd()), windows.CurrentProcess(), &files[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if files[i] != 0 {
				windows.CloseHandle(files[i])
			}
		}()
	}
	attributeCount := uint32(2)
	if assignment {
		attributeCount = 1
	}
	attrs, err := windows.NewProcThreadAttributeList(attributeCount)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if !assignment {
		if err := attrs.Update(windowsJobListAttribute, unsafe.Pointer(&owned.job), unsafe.Sizeof(owned.job)); err != nil {
			t.Fatal(err)
		}
	}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&files[0]), unsafe.Sizeof(files)); err != nil {
		t.Fatal(err)
	}
	argv := task43ProbeArgv(t, "descendant")
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		t.Fatal(err)
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := windows.UTF16PtrFromString(home)
	if err != nil {
		t.Fatal(err)
	}
	block, err := windowsEnvironmentBlock(accounts.RuntimeEnvironment(os.Environ(), env))
	if err != nil {
		t.Fatal(err)
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb = uint32(unsafe.Sizeof(info))
	info.Flags = windows.STARTF_USESTDHANDLES
	info.StdInput = files[0]
	info.StdOutput = files[1]
	info.StdErr = files[1]
	flags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if assignment {
		flags |= windows.CREATE_SUSPENDED
	}
	if err := windows.CreateProcess(app, command, nil, nil, true, flags, &block[0], dir, &info.StartupInfo, &owned.anchor); err != nil {
		t.Fatal(err)
	}
	if assignment {
		if err := windows.AssignProcessToJobObject(owned.job, owned.anchor.Process); err != nil {
			stopErr := windows.TerminateProcess(owned.anchor.Process, 1)
			_, waitErr := windows.WaitForSingleObject(owned.anchor.Process, 2000)
			t.Fatal("assign fresh suspended root", err, stopErr, waitErr)
		}
		if _, err := windows.ResumeThread(owned.anchor.Thread); err != nil {
			t.Fatal(err)
		}
	}
	var root windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), owned.anchor.Process, windows.CurrentProcess(), &root, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	owned.root = root
	for i := range files {
		windows.CloseHandle(files[i])
		files[i] = 0
	}
	writer.Close()
	output := &probeOutput{overflow: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(probeWriter{output: output, stdout: true}, reader); done <- err }()
	status, observeErr := windows.WaitForSingleObject(owned.root, 1000)
	t.Log("direct creation root wait", status, observeErr)
	cleanupErr := owned.retire(nil)
	readErr := <-done
	output.mu.Lock()
	t.Logf("direct output=%q cleanup=%v read=%v", output.stdout, cleanupErr, readErr)
	output.mu.Unlock()
	if status != windows.WAIT_OBJECT_0 || observeErr != nil {
		t.Fatal("direct-created root did not exit")
	}
}

func TestTask43WindowsJobRootExitDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, false, "")
}

func task43JobRootDiagnosis(t *testing.T, stages bool, variant string) {
	home := task43SessionHome(t)
	env, privateHome, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(privateHome)
	argv := task43ProbeArgv(t, "descendant")
	if stages {
		argv = []string{argv[0], "-test.run=^TestTask43WindowsJobDescendantDiagnosisFixture$", "--", "task43-job-diagnosis"}
		if variant == "null-child-io" || variant == "exiting-child" {
			argv = append(argv, variant)
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = accounts.RuntimeEnvironment(os.Environ(), env), home
	output := &probeOutput{overflow: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = probeWriter{output: output, stdout: true}, probeWriter{output: output, stdout: true}
	var owned *ownedProcess
	if variant == "hidden-console" {
		owned, err = newWindowsJob()
		if err == nil {
			err = owned.createAnchor(cmd)
		}
		if err == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{ParentProcess: syscall.Handle(owned.anchor.Process), HideWindow: true}
			err = cmd.Start()
		}
		if err == nil {
			err = owned.captureRoot(cmd)
		}
	} else {
		owned, err = startOwnedCommand(cmd)
	}
	if err != nil {
		if owned != nil {
			_ = owned.retire(nil)
		}
		t.Fatal(err)
	}
	if variant == "retired-anchor" {
		if err := windows.TerminateProcess(owned.anchor.Process, 0); err != nil {
			_ = owned.stop()
			_, _ = waitOwnedOutcome(cmd, owned)
			t.Fatal(err)
		}
	}
	waitMS := uint32(1000)
	if variant == "long-observation" {
		waitMS = 15000
	}
	start := time.Now()
	status, observeErr := windows.WaitForSingleObject(owned.root, waitMS)
	t.Log("observed root wait duration", time.Since(start))
	task43ObserveThreadWaits(t, uint32(cmd.Process.Pid))
	var accounting windowsJobAccounting
	queryErr := windows.QueryInformationJobObject(owned.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	var members struct {
		Assigned uint32
		Listed   uint32
		PIDs     [16]uintptr
	}
	if err := windows.QueryInformationJobObject(owned.job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&members)), uint32(unsafe.Sizeof(members)), nil); err != nil {
		t.Log("member observation", err)
	}
	for i := uint32(0); i < members.Listed && i < 16; i++ {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(members.PIDs[i]))
		if err != nil {
			t.Log("image observation", err)
			continue
		}
		var name [1024]uint16
		size := uint32(len(name))
		err = windows.QueryFullProcessImageName(h, 0, &name[0], &size)
		t.Logf("job member PID=%d image=%s query=%v", members.PIDs[i], windows.UTF16ToString(name[:size]), err)
		windows.CloseHandle(h)
	}
	output.mu.Lock()
	t.Logf("root PID=%d wait=%d error=%v active=%d query=%v output=%q", cmd.Process.Pid, status, observeErr, accounting.ActiveProcesses, queryErr, output.stdout)
	output.mu.Unlock()
	stopErr := owned.stop()
	cleanupErr, waitErr := waitOwnedOutcome(cmd, owned)
	output.mu.Lock()
	t.Logf("drained stop=%v cleanup=%v wait=%v root=%v output=%q", stopErr, cleanupErr, waitErr, cmd.ProcessState, output.stdout)
	output.mu.Unlock()
	if status != windows.WAIT_OBJECT_0 || observeErr != nil {
		t.Fatal("fixture root did not exit")
	}
}
