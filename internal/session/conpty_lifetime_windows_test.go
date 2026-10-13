package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsConsoleLifetimeFixture(t *testing.T) {
	if len(os.Args) < 4 || os.Args[3] != "task43-console-lifetime" {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode); err != nil {
		t.Fatal("lifetime helper is not attached to a native console", err)
	}
	argv := task43ProbeArgv(t, "hold")
	child := exec.Command(argv[0], argv[1:]...)
	child.Env, child.Stdout, child.Stderr = os.Environ(), os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	var created, exited, kernel, user windows.Filetime
	var observeErr error
	if err := child.Process.WithHandle(func(handle uintptr) {
		observeErr = windows.GetProcessTimes(windows.Handle(handle), &created, &exited, &kernel, &user)
	}); err != nil || observeErr != nil {
		t.Fatal(err, observeErr)
	}
	fmt.Fprintf(os.Stdout, "LIFETIME-CHILD:%d:%d\n", child.Process.Pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	fmt.Fprintln(os.Stdout, "LIFETIME-READY")
	if len(os.Args) == 5 && os.Args[4] == "flood" {
		block := strings.Repeat("0123456789ABCDEF", 256)
		for i := 0; i < 2048; i++ {
			if _, err := io.WriteString(os.Stdout, block); err != nil {
				t.Fatal(err)
			}
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "exit" {
			os.Exit(0)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func task43LifetimeArgv(t *testing.T, mode string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{exe, "-test.run=^TestTask43WindowsConsoleLifetimeFixture$", "--", "task43-console-lifetime"}
	if mode != "" {
		argv = append(argv, mode)
	}
	return argv
}

func task43ForeignSibling(t *testing.T) *exec.Cmd {
	t.Helper()
	argv := task43ProbeArgv(t, "hold")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "USERPROFILE=" + os.Getenv("USERPROFILE")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func task43SiblingStillRunning(t *testing.T, sibling *exec.Cmd) {
	t.Helper()
	var status uint32
	var observed error
	if err := sibling.Process.WithHandle(func(handle uintptr) {
		status, observed = windows.WaitForSingleObject(windows.Handle(handle), 0)
	}); err != nil || observed != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("foreign sibling was terminated", status, err, observed)
	}
}

func task43ObserveLifetimeChild(t *testing.T, output string) windows.Handle {
	t.Helper()
	receipt := regexp.MustCompile(`LIFETIME-CHILD:([0-9]+):([0-9]+)`).FindStringSubmatch(output)
	if len(receipt) != 3 {
		t.Fatal("owned child birth receipt absent", output)
	}
	pid, err := strconv.ParseUint(receipt[1], 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := strconv.ParseUint(receipt[2], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// The observed birth identity proves drain; it grants no control rights.
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(handle) })
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != stamp {
		t.Fatal("child birth identity was replaced", err)
	}
	if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("child was not alive before owned termination", status, err)
	}
	return handle
}

func TestTask43WindowsConPTYStopShutdownTree(t *testing.T) {
	for _, action := range []string{"stop", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			home := task43SessionHome(t)
			m := New(t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
				defer done()
				if err := m.Shutdown(cleanup); err != nil {
					t.Error(err)
				}
			}()
			foreign := task43ForeignSibling(t)
			pending, err := m.StartPending(domain.Session{ID: "owned-lifetime", Argv: task43LifetimeArgv(t, ""), Cols: 80, Rows: 24, Owner: "web", OwnerClient: "first", AccountID: "account"}, nil, home, 1)
			if err != nil {
				t.Fatal(err)
			}
			sub, err := m.SubscribeTerminal("owned-lifetime")
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			var output strings.Builder
			task43ConsoleReadUntil(t, ctx, sub, &output, "LIFETIME-READY")
			child := task43ObserveLifetimeChild(t, output.String())
			privateHome := pending.live.privateHome
			if privateHome == "" {
				t.Fatal("session private home was not recorded")
			}
			if action == "stop" {
				_, err = m.Stop("owned-lifetime")
			} else {
				err = m.Shutdown(ctx)
			}
			if err != nil {
				t.Fatal("bounded console termination", err)
			}
			if status, err := windows.WaitForSingleObject(child, 0); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("owned child survived console termination", status, err)
			}
			task43SiblingStillRunning(t, foreign)
			if pending.Snapshot().Process != "exited" || pending.live.cleanupErr != nil || !pending.live.owned.retired {
				t.Fatal("console termination did not complete", pending.Snapshot(), pending.live.cleanupErr)
			}
			if _, err := os.Stat(privateHome); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("private session home survived owned drain", err)
			}
			m.mu.Lock()
			admitted := m.accounts["account"]
			m.mu.Unlock()
			if admitted != 0 {
				t.Fatal("console termination retained account admission", admitted)
			}
		})
	}
}

func TestTask43WindowsConPTYUnreadOutputOverflow(t *testing.T) {
	home := task43SessionHome(t)
	selected, private, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(private)
	foreign := task43ForeignSibling(t)
	argv := task43LifetimeArgv(t, "flood")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = accounts.RuntimeEnvironment(os.Environ(), selected), home
	terminal, owned, err := startManagedPTY(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owned.retire(nil); err != nil {
			t.Error(err)
		}
		if err := terminal.Close(); err != nil && !errors.Is(err, ErrTerminalOutputOverflow) {
			t.Error(err)
		}
	}()
	// There is deliberately no host reader. The native output pump must bound
	// its queue, stop its owned clients, and continue draining the actual pipe.
	if status, err := windows.WaitForSingleObject(owned.root, 5000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("unread native console did not stop after bounded overflow", status, err)
	}
	if cleanup, wait := waitOwnedOutcome(cmd, owned); cleanup != nil || wait != nil {
		t.Fatal("overflow did not drain its whole original Job", cleanup, wait)
	}
	data, err := io.ReadAll(terminal)
	if !errors.Is(err, ErrTerminalOutputOverflow) || len(data) == 0 || len(data) > terminalQueueBytes {
		t.Fatal("unread native output lacked bounded visible overflow", len(data), err)
	}
	if err := terminal.Close(); !errors.Is(err, ErrTerminalOutputOverflow) {
		t.Fatal("overflow was lost during console close", err)
	}
	task43SiblingStillRunning(t, foreign)
}
