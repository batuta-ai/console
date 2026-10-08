package session

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsConPTYOriginalExitDiagnosis(t *testing.T) {
	home := task43SessionHome(t)
	m := New(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	p, err := m.StartPending(domain.Session{ID: "native-exit-observer", Argv: task43ConsoleArgv(t), Cols: 80, Rows: 24}, nil, home, 1)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.live.done:
		t.Logf("native original root pid=%d exited=%v code=%x cleanup=%v consoleError=%v replay=%q", p.live.owned.pid, p.live.owned.exited, p.live.owned.exitCode, p.live.cleanupErr, p.live.terminal.(*windowsTerminal).pumpErr, p.live.replay)
		t.Fatal("native console root exited before interactive input")
	case <-ctx.Done():
		if _, err := m.Stop("native-exit-observer"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTask43WindowsConPTYDirectBirthJobDiagnosis(t *testing.T) {
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
	console, err := newWindowsTerminal(80, 24, owned.stop)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := console.Close(); err != nil {
			t.Error(err)
		}
	}()
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		t.Fatal(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windowsJobListAttribute, unsafe.Pointer(&owned.job), unsafe.Sizeof(owned.job)); err != nil {
		t.Fatal(err)
	}
	update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	if ok, _, err := update.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console.console), unsafe.Sizeof(console.console), 0, 0); ok == 0 {
		t.Fatal(err)
	}
	argv := task43ConsoleArgv(t)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = accounts.RuntimeEnvironment(os.Environ(), env), home
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		t.Fatal(err)
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := windows.UTF16PtrFromString(cmd.Dir)
	if err != nil {
		t.Fatal(err)
	}
	block, err := windowsEnvironmentBlock(cmd.Env)
	if err != nil {
		t.Fatal(err)
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb = uint32(unsafe.Sizeof(info))
	if err := windows.CreateProcess(app, command, nil, nil, false, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &block[0], directory, &info.StartupInfo, &owned.anchor); err != nil {
		t.Fatal(err)
	}
	if err := owned.setAnchorOwner(); err != nil {
		t.Fatal(err)
	}
	if err := windows.DuplicateHandle(windows.CurrentProcess(), owned.anchor.Process, windows.CurrentProcess(), &owned.root, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	owned.pid, owned.native = owned.anchor.ProcessId, true
	if err := console.releaseConsoleEndpoints(); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.ResumeThread(owned.anchor.Thread); err != nil {
		t.Fatal(err)
	}
	output := &probeOutput{overflow: make(chan struct{})}
	readDone := make(chan error, 1)
	go func() { _, err := io.Copy(probeWriter{output: output, stdout: true}, console); readDone <- err }()
	status, err := windows.WaitForSingleObject(owned.root, 1000)
	var code uint32
	codeErr := windows.GetExitCodeProcess(owned.root, &code)
	output.mu.Lock()
	captured := string(output.stdout)
	output.mu.Unlock()
	t.Logf("direct birth-job native original root wait=%d error=%v exitCode=%x codeError=%v output=%q", status, err, code, codeErr, captured)
	stopErr := owned.retire(nil)
	closeErr := console.Close()
	readErr := <-readDone
	if stopErr != nil || closeErr != nil || readErr != nil || status != uint32(windows.WAIT_TIMEOUT) || !strings.Contains(captured, "CONPTY-READY:80:24") {
		t.Fatal("direct birth-job console did not remain interactive", stopErr, closeErr, readErr)
	}
}
