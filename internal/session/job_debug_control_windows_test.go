package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"golang.org/x/sys/windows"
)

type task43DebugEvent struct {
	Code, Process, Thread uint32
	Data                  [20]uint64
}

type task43DebugResult struct {
	RootExited bool
	Creations  int
	Remaining  int
	Handles    []windows.Handle
	Err        error
}

func TestTask43WindowsJobMatchedDebugControl(t *testing.T) {
	for _, job := range []bool{false, true} {
		t.Run(fmt.Sprint("job=", job), func(t *testing.T) {
			home := task43SessionHome(t)
			env, selected, err := accounts.SessionEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(selected)
			argv := task43ProbeArgv(t, "descendant")
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
			output := &probeOutput{overflow: make(chan struct{})}
			readDone := make(chan error, 1)
			go func() { _, err := io.Copy(probeWriter{output: output, stdout: true}, reader); readDone <- err }()
			result := make(chan task43DebugResult)
			go func() {
				// Exiting without UnlockOSThread destroys this dedicated debugger
				// thread. Its native kill-on-exit connection owns all descendants.
				runtime.LockOSThread()
				result <- task43RunDebugControl(argv, accounts.RuntimeEnvironment(os.Environ(), env), home, input, writer, job)
			}()
			got := <-result
			writer.Close()
			for _, handle := range got.Handles {
				status, err := windows.WaitForSingleObject(handle, 2000)
				windows.CloseHandle(handle)
				if err != nil || status != windows.WAIT_OBJECT_0 {
					t.Errorf("original debug-created process did not drain: %d %v", status, err)
				}
			}
			select {
			case err := <-readDone:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				reader.Close()
				t.Error("debug fixture output did not drain")
			}
			output.mu.Lock()
			t.Logf("job=%v rootExited=%v creations=%d remaining=%d output=%q error=%v", job, got.RootExited, got.Creations, got.Remaining, output.stdout, got.Err)
			output.mu.Unlock()
			if got.Err != nil || !got.RootExited || got.Creations < 2 || got.Remaining != 0 {
				t.Fatal("matched native process creation did not complete")
			}
		})
	}
}

func task43RunDebugControl(argv, env []string, dir string, input, output *os.File, useJob bool) (result task43DebugResult) {
	var owned *ownedProcess
	var err error
	if useJob {
		owned, err = newWindowsJob()
		if err != nil {
			result.Err = err
			return
		}
		defer func() { result.Err = errors.Join(result.Err, owned.closeHandles()) }()
	}
	var files [2]windows.Handle
	defer func() {
		for _, handle := range files {
			if handle != 0 {
				windows.CloseHandle(handle)
			}
		}
	}()
	for i, file := range []*os.File{input, output} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &files[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			result.Err = err
			return
		}
	}
	count := uint32(1)
	if useJob {
		count++
	}
	attrs, err := windows.NewProcThreadAttributeList(count)
	if err != nil {
		result.Err = err
		return
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&files[0]), unsafe.Sizeof(files)); err != nil {
		result.Err = err
		return
	}
	if useJob {
		if err := attrs.Update(windowsJobListAttribute, unsafe.Pointer(&owned.job), unsafe.Sizeof(owned.job)); err != nil {
			result.Err = err
			return
		}
	}
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		result.Err = err
		return
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		result.Err = err
		return
	}
	directory, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		result.Err = err
		return
	}
	block, err := windowsEnvironmentBlock(env)
	if err != nil {
		result.Err = err
		return
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb, info.Flags = uint32(unsafe.Sizeof(info)), windows.STARTF_USESTDHANDLES
	info.StdInput, info.StdOutput, info.StdErr = files[0], files[1], files[1]
	var root windows.ProcessInformation
	flags := uint32(windows.DEBUG_PROCESS | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(app, command, nil, nil, true, flags, &block[0], directory, &info.StartupInfo, &root); err != nil {
		result.Err = err
		return
	}
	defer windows.CloseHandle(root.Thread)
	defer windows.CloseHandle(root.Process)
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	ok, _, callErr := kernel.NewProc("DebugSetProcessKillOnExit").Call(1)
	if ok == 0 {
		result.Err = callErr
		return
	}
	for i, handle := range files {
		windows.CloseHandle(handle)
		files[i] = 0
	}
	live := map[uint32]windows.Handle{}
	deadline := time.Now().Add(3 * time.Second)
	stopping := false
	for {
		if !stopping && (result.RootExited || time.Now().After(deadline)) {
			stopping = true
			deadline = time.Now().Add(2 * time.Second)
			for _, handle := range live {
				result.Err = errors.Join(result.Err, windows.TerminateProcess(handle, 1))
			}
		}
		if stopping && (len(live) == 0 || time.Now().After(deadline)) {
			break
		}
		var event task43DebugEvent
		ok, _, callErr := kernel.NewProc("WaitForDebugEvent").Call(uintptr(unsafe.Pointer(&event)), 100)
		if ok == 0 {
			if callErr != windows.ERROR_SEM_TIMEOUT {
				result.Err = errors.Join(result.Err, callErr)
				break
			}
			continue
		}
		continueStatus := uintptr(0x00010002)
		switch event.Code {
		case 3:
			file, process := windows.Handle(event.Data[0]), windows.Handle(event.Data[1])
			if file != 0 {
				windows.CloseHandle(file)
			}
			var captured windows.Handle
			if err := windows.DuplicateHandle(windows.CurrentProcess(), process, windows.CurrentProcess(), &captured, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
				result.Err = errors.Join(result.Err, err)
			} else {
				result.Handles = append(result.Handles, captured)
				live[event.Process] = captured
				result.Creations++
				if stopping {
					result.Err = errors.Join(result.Err, windows.TerminateProcess(captured, 1))
				}
			}
		case 5:
			if event.Process == root.ProcessId {
				result.RootExited = true
			}
			delete(live, event.Process)
		case 6:
			if file := windows.Handle(event.Data[0]); file != 0 {
				windows.CloseHandle(file)
			}
		case 1:
			code := uint32(event.Data[0])
			if code != 0x80000003 && code != 0x80000004 {
				continueStatus = 0x80010001
			}
		}
		ok, _, callErr = kernel.NewProc("ContinueDebugEvent").Call(uintptr(event.Process), uintptr(event.Thread), continueStatus)
		if ok == 0 {
			result.Err = errors.Join(result.Err, callErr)
			break
		}
	}
	result.Remaining = len(live)
	return
}
