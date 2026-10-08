package session

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsJobDescendantDiagnosisFixture(t *testing.T) {
	if len(os.Args) < 4 || os.Args[3] != "task43-job-diagnosis" {
		return
	}
	if len(os.Args) > 4 && os.Args[4] == "native-child" {
		app, err := windows.UTF16PtrFromString(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		argv := []string{os.Args[0], "-test.run=^TestTask43WindowsProbeProcessFixture$", "--", "task43-probe-fixture", "hold"}
		command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
		if err != nil {
			t.Fatal(err)
		}
		block, err := windowsEnvironmentBlock(os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		info := windows.StartupInfo{}
		info.Cb = uint32(unsafe.Sizeof(info))
		var child windows.ProcessInformation
		fmt.Fprintln(os.Stdout, "before native child start")
		if err := windows.CreateProcess(app, command, nil, nil, false, windows.CREATE_NO_WINDOW|windows.CREATE_UNICODE_ENVIRONMENT, &block[0], nil, &info, &child); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "after native child start")
		var created, exited, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(child.Process, &created, &exited, &kernel, &user); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stdout, "%d:%d\nNATIVE-ROOT-EXIT\n", child.ProcessId, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
		windows.CloseHandle(child.Thread)
		windows.CloseHandle(child.Process)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stdout, "before child command")
	mode := "hold"
	if len(os.Args) > 4 && os.Args[4] == "exiting-child" {
		mode = "nonzero"
	}
	child := exec.Command(os.Args[0], "-test.run=^TestTask43WindowsProbeProcessFixture$", "--", "task43-probe-fixture", mode)
	if len(os.Args) > 4 && os.Args[4] == "system-child-image" {
		system, err := windows.GetSystemDirectory()
		if err != nil {
			t.Fatal(err)
		}
		child = exec.Command(filepath.Join(system, "whoami.exe"))
	}
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
	if len(os.Args) > 4 && os.Args[4] == "minimal-child-env" {
		child.Env = []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	}
	if len(os.Args) > 4 && os.Args[4] == "system-child-dir" {
		dir, err := windows.GetSystemDirectory()
		if err != nil {
			t.Fatal(err)
		}
		child.Dir = dir
	}
	if len(os.Args) > 4 && os.Args[4] == "headless-child" {
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	}
	if len(os.Args) > 4 && os.Args[4] == "detached-child" {
		child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS, HideWindow: true}
	}
	if len(os.Args) > 4 && os.Args[4] == "null-child-io" {
		child.Stdout, child.Stderr = nil, nil
	}
	fmt.Fprintln(os.Stdout, "before child start")
	if len(os.Args) > 4 && os.Args[4] == "thread-labeled" {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		fmt.Fprintf(os.Stdout, "native creator thread=%d\n", windows.GetCurrentThreadId())
	}
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

func TestTask43WindowsJobHeadlessChildDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "headless-child")
}

func TestTask43WindowsJobNativeDescendantDiagnosis(t *testing.T) {
	task43JobRootDiagnosis(t, true, "native-child")
}

func TestTask43WindowsJobMinimalChildEnvironment(t *testing.T) {
	task43JobRootDiagnosis(t, true, "minimal-child-env")
}

func TestTask43WindowsJobSystemChildDirectory(t *testing.T) {
	task43JobRootDiagnosis(t, true, "system-child-dir")
}

func TestTask43WindowsJobSystemChildImage(t *testing.T) {
	task43JobRootDiagnosis(t, true, "system-child-image")
}

func TestTask43WindowsJobDetachedRootConsole(t *testing.T) {
	task43JobRootDiagnosis(t, true, "detached-root")
}

func TestTask43WindowsJobNewRootConsole(t *testing.T) {
	task43JobRootDiagnosis(t, true, "new-root-console")
}

func TestTask43WindowsJobDetachedChildConsole(t *testing.T) {
	task43JobRootDiagnosis(t, true, "detached-child")
}

func TestTask43WindowsJobRootWithoutTerm(t *testing.T) {
	task43JobRootDiagnosis(t, true, "root-without-term")
}

func TestTask43WindowsJobRootIdentityOnly(t *testing.T) {
	task43JobRootDiagnosis(t, true, "root-identity-only")
}

func TestTask43WindowsJobRootSystemDirectory(t *testing.T) {
	task43JobRootDiagnosis(t, true, "root-system-dir")
}

func TestTask43WindowsJobDetachedThreadState(t *testing.T) {
	task43JobRootDiagnosis(t, true, "detached-thread-state")
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

func task43ObserveRootModulesAndToken(t *testing.T, root windows.Handle) {
	var modules [128]windows.Handle
	var needed uint32
	if err := windows.EnumProcessModulesEx(root, &modules[0], uint32(unsafe.Sizeof(modules)), &needed, 3); err != nil {
		t.Log("module observation", err)
	} else {
		for i := uint32(0); i < needed/uint32(unsafe.Sizeof(modules[0])) && i < 128; i++ {
			var name [1024]uint16
			if err := windows.GetModuleFileNameEx(root, modules[i], &name[0], uint32(len(name))); err != nil {
				t.Log("module observation", err)
			} else {
				t.Log("root module", filepath.Base(windows.UTF16ToString(name[:])))
			}
		}
	}
	var token windows.Token
	if err := windows.OpenProcessToken(root, windows.TOKEN_QUERY, &token); err != nil {
		t.Log("token observation", err)
		return
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Log(err)
		return
	}
	t.Log("root token user", user.User.Sid.String())
	var n uint32
	_ = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &n)
	buf := make([]byte, n)
	if len(buf) > 0 {
		if err := windows.GetTokenInformation(token, windows.TokenOwner, &buf[0], n, &n); err != nil {
			t.Log(err)
		} else {
			sid := *(**windows.SID)(unsafe.Pointer(&buf[0]))
			t.Log("root token default owner", sid.String())
		}
	}
	stats, err := windowsTokenState(token)
	if err != nil {
		t.Log(err)
	} else {
		t.Log("root token instance", stats.ID, "modified", stats.Modified)
	}
}

func TestTask43WindowsJobDirectCreationDiagnosis(t *testing.T) {
	task43DirectJobDiagnosis(t, true, false, false)
}

func TestTask43WindowsJobWithoutCompletionPortDiagnosis(t *testing.T) {
	task43DirectJobDiagnosis(t, false, false, false)
}

func TestTask43WindowsJobSuspendedAssignmentDiagnosis(t *testing.T) {
	task43DirectJobDiagnosis(t, true, true, false)
}

func TestTask43WindowsJobOriginalThreadSnapshot(t *testing.T) {
	task43DirectJobDiagnosis(t, true, false, true)
}

func task43DirectJobDiagnosis(t *testing.T, completion, assignment, snapshot bool) {
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
	if snapshot {
		argv = []string{argv[0], "-test.run=^TestTask43WindowsJobDescendantDiagnosisFixture$", "--", "task43-job-diagnosis", "thread-labeled"}
	}
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
	if snapshot {
		output.mu.Lock()
		capturedOutput := string(output.stdout)
		output.mu.Unlock()
		task43ObserveOriginalCreationThread(t, owned.anchor, capturedOutput)
	}
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

func TestTask43WindowsJobShortPrivatePathsDiagnosis(t *testing.T) {
	for _, variant := range []string{"root-short-temp", "root-short-identity"} {
		t.Run(variant, func(t *testing.T) { task43JobRootDiagnosis(t, true, variant) })
	}
}

func task43JobRootDiagnosis(t *testing.T, stages bool, variant string) {
	home := task43SessionHome(t)
	shortHome := ""
	if variant == "root-short-temp" || variant == "root-short-identity" {
		shortHome = filepath.Join(os.Getenv("USERPROFILE"), "bc43-"+rand.Text())
		if err := consoleruntime.EnsurePrivateDirectory(shortHome); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(shortHome)
		if variant == "root-short-identity" {
			t.Setenv("LOCALAPPDATA", shortHome)
		}
	}
	env, privateHome, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(privateHome)
	argv := task43ProbeArgv(t, "descendant")
	if stages {
		argv = []string{argv[0], "-test.run=^TestTask43WindowsJobDescendantDiagnosisFixture$", "--", "task43-job-diagnosis"}
		if variant == "null-child-io" || variant == "exiting-child" || variant == "headless-child" || variant == "native-child" || variant == "minimal-child-env" || variant == "system-child-dir" || variant == "system-child-image" || variant == "detached-child" {
			argv = append(argv, variant)
		}
		if variant == "detached-thread-state" {
			argv = append(argv, "thread-labeled")
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = accounts.RuntimeEnvironment(os.Environ(), env), home
	if variant == "root-short-temp" {
		for i, entry := range cmd.Env {
			key, _, _ := strings.Cut(entry, "=")
			if key == "TEMP" || key == "TMP" || key == "TMPDIR" {
				cmd.Env[i] = key + "=" + shortHome
			}
		}
	}
	if variant == "root-short-temp" || variant == "root-short-identity" {
		for _, entry := range cmd.Env {
			key, value, _ := strings.Cut(entry, "=")
			if key == "TEMP" || key == "TMP" || key == "USERPROFILE" || key == "LOCALAPPDATA" {
				t.Logf("controlled root path key=%s UTF16length=%d", key, len(utf16.Encode([]rune(value))))
			}
		}
	}
	if variant == "root-system-dir" {
		cmd.Dir, err = windows.GetSystemDirectory()
		if err != nil {
			t.Fatal(err)
		}
	}
	if variant == "root-without-term" || variant == "root-identity-only" {
		kept := make([]string, 0, len(cmd.Env))
		for _, entry := range cmd.Env {
			key, _, _ := strings.Cut(entry, "=")
			keep := key != "TERM"
			if variant == "root-identity-only" {
				keep = key == "SYSTEMROOT" || key == "WINDIR" || key == "SYSTEMDRIVE" || key == "COMSPEC" || key == "HOME" || key == "USERPROFILE" || key == "TEMP" || key == "TMP" || key == "APPDATA" || key == "LOCALAPPDATA" || key == "CODEX_HOME" || key == "CLAUDE_CONFIG_DIR" || key == "XDG_CONFIG_HOME" || key == "XDG_DATA_HOME" || key == "XDG_CACHE_HOME" || key == "XDG_STATE_HOME" || key == "HOMEDRIVE" || key == "HOMEPATH"
			}
			if keep {
				kept = append(kept, entry)
			}
		}
		cmd.Env = kept
	}
	output := &probeOutput{overflow: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = probeWriter{output: output, stdout: true}, probeWriter{output: output, stdout: true}
	var owned *ownedProcess
	if variant == "hidden-console" || variant == "detached-root" || variant == "new-root-console" || variant == "detached-thread-state" {
		owned, err = newWindowsJob()
		if err == nil {
			err = owned.createAnchor(cmd)
		}
		if err == nil {
			flags := uint32(0)
			if variant == "detached-root" || variant == "detached-thread-state" {
				flags = windows.DETACHED_PROCESS
			}
			if variant == "new-root-console" {
				flags = windows.CREATE_NEW_CONSOLE
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{ParentProcess: syscall.Handle(owned.anchor.Process), CreationFlags: flags, HideWindow: true}
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
	task43ObserveRootModulesAndToken(t, owned.root)
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
	if variant == "detached-thread-state" {
		pids := make(map[uintptr]bool)
		for i := uint32(0); i < members.Listed && i < 16; i++ {
			pids[members.PIDs[i]] = true
		}
		task43ObserveNativeThreadState(t, pids, owned.root)
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
