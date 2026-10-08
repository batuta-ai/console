package session

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsJobChildBirthFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-child-job-birth" {
		return
	}
	handle, err := strconv.ParseUint(os.Args[4], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	job := windows.Handle(handle)
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windowsJobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		t.Fatal(err)
	}
	argv := task43ProbeArgv(t, "hold")
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		t.Fatal(err)
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		t.Fatal(err)
	}
	block, err := windowsEnvironmentBlock(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb = uint32(unsafe.Sizeof(info))
	var child windows.ProcessInformation
	fmt.Fprintln(os.Stdout, "before birth-assigned native child")
	if err := windows.CreateProcess(app, command, nil, nil, false, windows.CREATE_NO_WINDOW|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &block[0], nil, &info.StartupInfo, &child); err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(child.Thread)
	windows.CloseHandle(child.Process)
	windows.CloseHandle(job)
	fmt.Fprintln(os.Stdout, "after birth-assigned native child")
	os.Exit(0)
}

func TestTask43WindowsJoblessRootChildAtBirth(t *testing.T) {
	home := task43SessionHome(t)
	env, selected, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(selected)
	owned, err := newWindowsJob()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !owned.retired {
			if err := owned.retire(nil); err != nil {
				t.Error(err)
			}
		}
	}()
	var inherited windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), owned.job, windows.CurrentProcess(), &inherited, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inherited)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestTask43WindowsJobChildBirthFixture$", "--", "task43-child-job-birth", strconv.FormatUint(uint64(inherited), 10))
	cmd.Env, cmd.Dir = accounts.RuntimeEnvironment(os.Environ(), env), home
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true, AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(inherited)}}
	output := &probeOutput{overflow: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = probeWriter{output: output, stdout: true}, probeWriter{output: output, stdout: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var captureErr error
	if err := cmd.Process.WithHandle(func(handle uintptr) {
		captureErr = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(), &owned.root, 0, false, windows.DUPLICATE_SAME_ACCESS)
	}); err != nil || captureErr != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err, captureErr)
	}
	status, waitErr := windows.WaitForSingleObject(owned.root, 1500)
	if status != windows.WAIT_OBJECT_0 || waitErr != nil {
		if err := cmd.Process.Kill(); err != nil {
			t.Error(err)
		}
	}
	cleanupErr := owned.retire(nil)
	rootErr := cmd.Wait()
	output.mu.Lock()
	t.Logf("jobless root original wait=%d error=%v cleanup=%v root=%v output=%q", status, waitErr, cleanupErr, rootErr, output.stdout)
	output.mu.Unlock()
	if status != windows.WAIT_OBJECT_0 || waitErr != nil || cleanupErr != nil || rootErr != nil {
		t.Fatal("jobless root did not create its birth-contained child")
	}
}
