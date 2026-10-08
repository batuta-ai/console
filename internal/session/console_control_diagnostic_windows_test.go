package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsConsoleSignalDiagnosticFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-console-signal-diagnostic" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	name, _ := windows.UTF16PtrFromString("CONIN$")
	input, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal("attached console input", err)
	}
	defer windows.CloseHandle(input)
	var mode uint32
	if err := windows.GetConsoleMode(input, &mode); err != nil {
		t.Fatal("attached console mode", err)
	}
	var clients [16]uint32
	count, _, listErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList").Call(uintptr(unsafe.Pointer(&clients[0])), uintptr(len(clients)))
	fmt.Fprintf(os.Stdout, "MODE:%d CLIENTS:%d:%v LISTERR:%v\n", mode, count, clients, listErr)
	if os.Args[4] == "enable" {
		if err := windows.SetConsoleMode(input, mode|windows.ENABLE_PROCESSED_INPUT); err != nil {
			t.Fatal("enable processing", err)
		}
		if ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler").Call(0, 0); ok == 0 {
			t.Fatal("clear inherited ignore", err)
		}
	}
	fmt.Fprint(os.Stdout, "LIFETIME-READY\n")
	if os.Args[4] == "selfevent" {
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0); err != nil {
			t.Fatal("owned console self event", err)
		}
	}
	<-ctx.Done()
	fmt.Fprint(os.Stdout, "SIGNAL-RECEIVED")
	os.Exit(0)
}

func TestTask43WindowsConsoleSignalDiagnostic(t *testing.T) {
	for _, mode := range []string{"default", "enable", "selfevent"} {
		t.Run(mode, func(t *testing.T) {
			env, home, err := accounts.SessionEnvironment()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(home) })
			m := New(t.TempDir())
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				t.Log("diagnostic drain", m.Shutdown(ctx))
			})
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			output := &task43LifetimeOutput{ready: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command := publication.Command{Executable: exe, Args: []string{"-test.run=^TestTask43WindowsConsoleSignalDiagnosticFixture$", "--", "task43-console-signal-diagnostic", mode}, Observer: output}
			type completion struct {
				result publication.CommandResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, err := (NativeRunner{Manager: m, Environment: env, Graceful: true}).Run(ctx, command)
				done <- completion{result, err}
			}()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-output.ready:
				cancel()
			case finished := <-done:
				t.Fatal("diagnostic ended before ready", finished)
			case <-timer.C:
				t.Fatal("diagnostic never ready")
			}
			select {
			case finished := <-done:
				t.Logf("stdout=%q stderr=%q exit=%d err=%v", finished.result.Stdout, finished.result.Stderr, finished.result.ExitCode, finished.err)
				if !strings.Contains(string(finished.result.Stdout), "SIGNAL-RECEIVED") || errors.Is(finished.err, ErrNativeCleanupUnresolved) {
					t.Error("native signal control did not cooperate")
				}
			case <-timer.C:
				t.Fatal("diagnostic never done")
			}
		})
	}
}
