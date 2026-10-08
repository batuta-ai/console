package session

import (
	"fmt"
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
