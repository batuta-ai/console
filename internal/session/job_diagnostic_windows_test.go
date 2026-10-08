package session

import (
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
	child := exec.Command(os.Args[0], "-test.run=^TestTask43WindowsProbeProcessFixture$", "--", "task43-probe-fixture", "hold")
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
		if variant == "null-child-io" {
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
	status, observeErr := windows.WaitForSingleObject(owned.root, 1000)
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
