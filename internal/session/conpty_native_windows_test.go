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

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsConsoleFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-native-console" {
		return
	}
	input, output := windows.Handle(os.Stdin.Fd()), windows.Handle(os.Stdout.Fd())
	var inputMode, outputMode uint32
	if err := windows.GetConsoleMode(input, &inputMode); err != nil {
		t.Fatal("native console input", err)
	}
	if err := windows.GetConsoleMode(output, &outputMode); err != nil {
		t.Fatal("native console output", err)
	}
	geometry := func(tag string) {
		var info windows.ConsoleScreenBufferInfo
		if err := windows.GetConsoleScreenBufferInfo(output, &info); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stdout, "%s:%d:%d\n", tag, info.Size.X, info.Size.Y)
	}
	geometry("CONPTY-READY")
	fmt.Fprintf(os.Stdout, "CONPTY-ARG:%s\n", os.Args[4])
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "size":
			geometry("CONPTY-SIZE")
		case "exit":
			fmt.Fprintln(os.Stdout, "CONPTY-FINAL")
			os.Exit(0)
		case "spawn-and-exit":
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
			fmt.Fprintf(os.Stdout, "CONPTY-CHILD:%d:%d\nCONPTY-FINAL\n", child.Process.Pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stdout, "CONPTY-ECHO:%s\n", line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func task43ConsoleArgv(t *testing.T) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, "-test.run=^TestTask43WindowsConsoleFixture$", "--", "task43-native-console", "literal & % ! ç \" \\"}
}

func task43ConsoleReadUntil(t *testing.T, ctx context.Context, sub *TerminalSubscription, text *strings.Builder, marker string) {
	t.Helper()
	for !strings.Contains(text.String(), marker) {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("console marker %q missing: output=%q error=%v", marker, text.String(), err)
		}
		text.Write(event.Data)
	}
}

func task43ConsoleStart(t *testing.T, id string) (*Manager, *Pending, *TerminalSubscription, context.Context) {
	t.Helper()
	home := task43SessionHome(t)
	m := New(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := m.Shutdown(shutdown); err != nil {
			t.Error(err)
		}
	})
	pending, err := m.StartPending(domain.Session{ID: id, Argv: task43ConsoleArgv(t), Cols: 80, Rows: 24, Owner: "web", OwnerClient: "first"}, nil, home, 1)
	if err != nil {
		t.Fatal("native ConPTY start", err)
	}
	if pending.Snapshot().PID <= 0 {
		t.Fatal("native creation did not publish its observed PID")
	}
	sub, err := m.SubscribeTerminal(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return m, pending, sub, ctx
}

func TestTask43WindowsConPTYInputResizeReplay(t *testing.T) {
	m, pending, sub, ctx := task43ConsoleStart(t, "native-input-resize")
	var output strings.Builder
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-READY:80:24")
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-ARG:literal & % ! ç \" \\")
	if err := m.InputContext(ctx, "native-input-resize", "foreign", []byte("forbidden\r")); !errors.Is(err, os.ErrPermission) {
		t.Fatal("foreign console input admitted", err)
	}
	if err := m.InputContext(ctx, "native-input-resize", "first", []byte("hello ç\r")); err != nil {
		t.Fatal(err)
	}
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-ECHO:hello ç")
	if err := m.Resize("native-input-resize", "first", 111, 37); err != nil {
		t.Fatal(err)
	}
	if err := m.InputContext(ctx, "native-input-resize", "first", []byte("size\r")); err != nil {
		t.Fatal(err)
	}
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-SIZE:111:37")
	old, err := m.PrepareTerminalInput("native-input-resize", "first", []byte("stale\r"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Take("native-input-resize", "tui", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Take("native-input-resize", "web", "first"); err != nil {
		t.Fatal(err)
	}
	if err := old.Write(ctx); !errors.Is(err, os.ErrPermission) {
		t.Fatal("old input generation became valid again", err)
	}
	reconnected, err := m.SubscribeTerminal("native-input-resize")
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	var replay strings.Builder
	task43ConsoleReadUntil(t, ctx, reconnected, &replay, "CONPTY-ECHO:hello ç")
	if strings.Contains(replay.String(), "CONPTY-ECHO:stale") || pending.Snapshot().Cols != 111 || pending.Snapshot().Rows != 37 {
		t.Fatal("replay/control geometry contract violated", replay.String())
	}
	if err := m.InputContext(ctx, "native-input-resize", "first", []byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-FINAL")
	select {
	case <-pending.live.done:
	case <-ctx.Done():
		t.Fatal("natural console exit did not drain", ctx.Err())
	}
	if pending.Snapshot().Process != "exited" || !pending.live.owned.retired || pending.live.cleanupErr != nil {
		t.Fatal("native console retirement incomplete", pending.Snapshot(), pending.live.cleanupErr)
	}
	if _, err := pending.live.pty.Stat(); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatal("native console output handle still open", err)
	}
	if err := pending.live.pty.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("native console Go file was not already closed", err)
	}
	for {
		event, err := sub.Next(ctx)
		output.Write(event.Data)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("terminal subscription did not finish", err)
		}
	}
	if strings.Contains(output.String(), "CONPTY-ECHO:stale") {
		t.Fatal("revoked bytes reached the native console", output.String())
	}
}

func TestTask43WindowsConPTYNaturalTreePreservesSibling(t *testing.T) {
	m, pending, sub, ctx := task43ConsoleStart(t, "native-natural-tree")
	argv := task43ProbeArgv(t, "hold")
	sibling := exec.Command(argv[0], argv[1:]...)
	sibling.Env = []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "USERPROFILE=" + os.Getenv("USERPROFILE")}
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { sibling.Process.Kill(); sibling.Wait() }()
	var output strings.Builder
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-READY:80:24")
	if err := m.InputContext(ctx, "native-natural-tree", "first", []byte("spawn-and-exit\r")); err != nil {
		t.Fatal(err)
	}
	task43ConsoleReadUntil(t, ctx, sub, &output, "CONPTY-FINAL")
	select {
	case <-pending.live.done:
	case <-ctx.Done():
		t.Fatal("natural root/descendant drain timed out", ctx.Err())
	}
	receipt := regexp.MustCompile(`CONPTY-CHILD:([0-9]+):([0-9]+)`).FindStringSubmatch(output.String())
	if len(receipt) != 3 {
		t.Fatal("native child birth receipt missing", output.String())
	}
	pid, err := strconv.ParseUint(receipt[1], 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := strconv.ParseUint(receipt[2], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	// A PID is observational only; termination belongs to the original Job.
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(handle)
		var created, exited, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
			t.Fatal(err)
		}
		if uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) == stamp {
			if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatal("owned console descendant survived", status, err)
			}
		}
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatal(err)
	}
	var status uint32
	var observeErr error
	if err := sibling.Process.WithHandle(func(handle uintptr) {
		status, observeErr = windows.WaitForSingleObject(windows.Handle(handle), 0)
	}); err != nil || observeErr != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("foreign sibling was affected", status, err, observeErr)
	}
	if pending.Snapshot().Process != "exited" || !pending.live.owned.retired || pending.live.cleanupErr != nil {
		t.Fatal("native tree retirement incomplete", pending.Snapshot(), pending.live.cleanupErr)
	}
}
