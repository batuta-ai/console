//go:build darwin || linux

package tui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/termios"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func terminalHostState(fd uintptr) (*unix.Termios, error) {
	state, err := termios.GetTermios(int(fd))
	if err == nil {
		terminalHostAttributes(state)
	}
	return state, err
}

func terminalHostAttributes(state *unix.Termios) {
	// Darwin sets PENDIN when restoring ICANON. It is pending-input state,
	// rather than an attribute selected by the program. Preserve all other
	// flags, control characters and speeds in the strict comparison.
	// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/tty.c
	if runtime.GOOS == "darwin" {
		state.Lflag &^= unix.PENDIN
	}
}

func TestTerminalHostAttributesPreserveConfiguration(t *testing.T) {
	before := unix.Termios{Iflag: 123, Oflag: 456, Cflag: 789, Lflag: unix.ECHO | unix.ICANON | unix.ISIG, Ispeed: 9600, Ospeed: 9600}
	before.Cc[unix.VINTR] = 3
	transient := before
	transient.Lflag |= unix.PENDIN
	terminalHostAttributes(&transient)
	if runtime.GOOS == "darwin" && !reflect.DeepEqual(before, transient) {
		t.Fatal("Darwin pending-input state was compared as configuration")
	}
	if runtime.GOOS != "darwin" && transient.Lflag&unix.PENDIN == 0 {
		t.Fatal("non-Darwin attribute was ignored")
	}
	changedAttributes := []unix.Termios{before, before, before}
	changedAttributes[0].Lflag &^= unix.ECHO
	changedAttributes[1].Lflag &^= unix.ICANON
	changedAttributes[2].Lflag &^= unix.ISIG
	for index, changed := range changedAttributes {
		terminalHostAttributes(&changed)
		if reflect.DeepEqual(before, changed) {
			t.Fatalf("lost host attribute %d was ignored", index)
		}
	}
}

func TestTerminalSignalHelper(t *testing.T) {
	if os.Getenv("CONSOLE_TERMINAL_SIGNAL_FIXTURE") != "1" {
		return
	}
	report := os.NewFile(3, "report")
	defer report.Close()
	gate := os.NewFile(4, "gate")
	defer gate.Close()
	before, err := terminalHostState(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(report, "PREPARED")
	var goByte [1]byte
	if _, err = io.ReadFull(gate, goByte[:]); err != nil {
		t.Fatal(err)
	}
	m, manager := terminalAttachedFixture(t)
	m.link.offer(terminalAction{operation: "tui"})
	f := terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	m.termFrame = f
	m.termMode = true
	m.screen = scrRoom
	if err = runTerminalProgram(m, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout)); err != nil {
		t.Fatal(err)
	}
	after, err := terminalHostState(os.Stdin.Fd())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("host terminal not restored: %v", err)
	}
	for _, id := range []string{"a-unrelated", "z-selected"} {
		s, ok := manager.Snapshot(id)
		if !ok || s.Process != "running" || s.OwnerClient != "" {
			t.Fatalf("outer signal changed child/control: %+v", s)
		}
	}
	fmt.Fprintln(report, "CLEAN")
}

func TestTerminalOuterSignalsRestoreHost(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGTSTP} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTerminalSignalHelper$", "-test.v")
			home := t.TempDir()
			cmd.Env = append(os.Environ(), "CONSOLE_TERMINAL_SIGNAL_FIXTURE=1", "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "TERM=xterm-256color")
			reportR, reportW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reportR.Close()
			gateR, gateW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer gateW.Close()
			cmd.ExtraFiles = []*os.File{reportW, gateR}
			master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 100})
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			reportW.Close()
			gateR.Close()
			defer func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			reports := make(chan string, 4)
			go func() {
				defer close(reports)
				scanner := bufio.NewScanner(reportR)
				for scanner.Scan() {
					reports <- scanner.Text()
				}
			}()
			select {
			case got := <-reports:
				if got != "PREPARED" {
					t.Fatalf("helper preparation: %q", got)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			before, err := terminalHostState(master.Fd())
			if err != nil {
				t.Fatal(err)
			}
			output := make(chan string, 128)
			go func() {
				defer close(output)
				b := make([]byte, 4096)
				for {
					n, err := master.Read(b)
					if n > 0 {
						select {
						case output <- string(b[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			gateW.Write([]byte{'G'})
			var transcript bytes.Buffer
			await := func(marker string) {
				t.Helper()
				for !strings.Contains(transcript.String(), marker) {
					select {
					case s, ok := <-output:
						if !ok {
							t.Fatalf("outer exited before %q: %s", marker, transcript.String())
						}
						transcript.WriteString(s)
					case <-ctx.Done():
						t.Fatalf("waiting for %q: %s", marker, transcript.String())
					}
				}
			}
			await("READY")
			if err = cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if sig == syscall.SIGTSTP {
				stopped := make(chan error, 1)
				go func() {
					var status syscall.WaitStatus
					_, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WUNTRACED, nil)
					// Go's BSD WaitStatus labels SIGSTOP as Continued. This
					// WUNTRACED-only wait cannot report a continuation.
					stoppedBySIGSTOP := runtime.GOOS == "darwin" && status.Continued()
					if err == nil && !status.Stopped() && !stoppedBySIGSTOP {
						err = fmt.Errorf("UI did not stop: %v", status)
					}
					stopped <- err
				}()
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("suspend did not stop")
				}
				suspended, err := terminalHostState(master.Fd())
				if err != nil || !reflect.DeepEqual(before, suspended) {
					t.Fatalf("suspend did not restore host: %v", err)
				}
				if err = cmd.Process.Signal(syscall.SIGCONT); err != nil {
					t.Fatal(err)
				}
				await("explicit reconnect required")
				if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-reports:
				if got != "CLEAN" {
					for chunk := range output {
						transcript.WriteString(chunk)
					}
					t.Fatalf("cleanup not confirmed: %q, process=%v, output=%s", got, cmd.Wait(), transcript.String())
				}
			case <-ctx.Done():
				t.Fatalf("cleanup timed out: %s", transcript.String())
			}
			if err = cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
