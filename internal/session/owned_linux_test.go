package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/unix"
)

func TestProcDisappearanceDuringRead(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	stat, err := os.Open(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer stat.Close()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("terminated fixture exited successfully")
	}
	for _, restricted := range []bool{false, true} {
		t.Run(strconv.FormatBool(restricted), func(t *testing.T) {
			root := t.TempDir()
			pid := cmd.Process.Pid
			dir := filepath.Join(root, strconv.Itoa(pid))
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(fmt.Sprintf("/proc/self/fd/%d", stat.Fd()), filepath.Join(dir, "stat")); err != nil {
				t.Fatal(err)
			}
			mount := "proc /proc proc rw 0 0\n"
			if restricted {
				mount = "proc /proc proc rw,hidepid=2 0 0\n"
			}
			if err := os.WriteFile(filepath.Join(root, "mounts"), []byte(mount), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := os.ReadFile(filepath.Join(dir, "stat")); !errors.Is(err, unix.ESRCH) {
				t.Fatalf("fixture did not produce real procfs ESRCH: %v", err)
			}
			observed, err := inspectProc(root, pid)
			if restricted {
				if err == nil || observed.Missing {
					t.Fatalf("restricted proc disappearance guessed absence: %+v %v", observed, err)
				}
			} else if err != nil || !observed.Missing || observed.Identity != "" {
				t.Fatalf("departed proc read: %+v %v", observed, err)
			}
		})
	}
}

func TestOwnedGroupExitRequiresVerifiedTerminalMembers(t *testing.T) {
	for _, tc := range []struct {
		name, mount, state, threads   string
		badStat, unreadable, terminal bool
	}{
		{name: "hidden", mount: "rw,hidepid=2", state: "Z", threads: "1"},
		{name: "unavailable", state: "Z", threads: "1"},
		{name: "malformed", mount: "rw", badStat: true},
		{name: "unreadable", mount: "rw", unreadable: true},
		{name: "invalid threads", mount: "rw", state: "Z", threads: "bad"},
		{name: "live", mount: "rw", state: "S", threads: "1"},
		{name: "zombie with remaining threads", mount: "rw", state: "Z", threads: "2"},
		{name: "terminal with unrelated live process", mount: "rw", state: "Z", threads: "1", terminal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.mount != "" {
				if err := os.WriteFile(filepath.Join(root, "mounts"), []byte("proc /proc proc "+tc.mount+" 0 0\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeRecord := func(pid, group int, state, threads string) string {
				t.Helper()
				dir := filepath.Join(root, strconv.Itoa(pid))
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				stat := procStat(pid, "group fixture", state, "123")
				close := strings.LastIndexByte(stat, ')')
				fields := strings.Fields(stat[close+1:])
				fields[2], fields[17] = strconv.Itoa(group), threads
				path := filepath.Join(dir, "stat")
				if err := os.WriteFile(path, []byte(stat[:close+1]+" "+strings.Join(fields, " ")), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			path := writeRecord(42, 42, tc.state, tc.threads)
			writeRecord(43, 777, "S", "1")
			if tc.badStat {
				if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unreadable {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := waitProcGroupExit(root, 42)
			if tc.terminal {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unverified group reported completed termination")
			}
			if (tc.name == "live" || tc.name == "zombie with remaining threads") && !errors.Is(err, errGroupExiting) {
				t.Fatalf("pending termination lost bounded cleanup error: %v", err)
			}
		})
	}
}

func TestOwnedGroupExitDoesNotCompleteWithUnresolvedKernelGroup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mounts"), []byte("proc /proc proc rw 0 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "42"), 0700); err != nil {
		t.Fatal(err)
	}
	stat := procStat(42, "exiting", "R", "123")
	close := strings.LastIndexByte(stat, ')')
	fields := strings.Fields(stat[close+1:])
	fields[2], fields[3], fields[17] = "-1", "-1", "0"
	if err := os.WriteFile(filepath.Join(root, "42", "stat"), []byte(stat[:close+1]+" "+strings.Join(fields, " ")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitProcGroupExit(root, 42); !errors.Is(err, errGroupExiting) {
		t.Fatalf("unresolved kernel membership must remain pending: %v", err)
	}
}

func TestNativeCancellationWaitsForDetachedDescendantTermination(t *testing.T) {
	for i := range 100 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			m := New(t.TempDir())
			dir := t.TempDir()
			childMarker, leaderMarker := filepath.Join(dir, "child"), filepath.Join(dir, "leader")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := `printf '%s' "$$" > "$2.tmp"; mv "$2.tmp" "$2"; "$3" -test.run='^TestProbeProcessHelper$' -- hold "$1" </dev/null >/dev/null 2>&1 & wait`
			go func() {
				_, err := (NativeRunner{Manager: m, Environment: []string{"HOME=" + dir}}).Run(ctx, publication.Command{
					Executable: "/bin/sh", Args: []string{"-c", script, "leader", childMarker, leaderMarker, executable},
				})
				done <- err
			}()
			child, leader := waitProbePID(t, childMarker), waitProbePID(t, leaderMarker)
			fd, err := unix.PidfdOpen(child, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); _ = unix.Close(fd) })
			if group, err := unix.Getpgid(child); err != nil || group != leader {
				t.Fatalf("fixture group %d leader %d: %v", group, leader, err)
			}
			if err := unix.PidfdSendSignal(fd, unix.SIGSTOP, nil, 0); err != nil {
				t.Fatal(err)
			}
			deadline, ticker := time.NewTimer(5*time.Second), time.NewTicker(time.Millisecond)
			defer deadline.Stop()
			defer ticker.Stop()
			for {
				stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
				if err != nil {
					t.Fatal(err)
				}
				fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
				if len(fields) > 0 && fields[0] == "T" {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("descendant did not reach stopped readiness")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNativeCleanupUnresolved) {
					t.Fatalf("native cancellation: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native cancellation did not finish")
			}
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if n, err := unix.Poll(fds, 0); err != nil || n != 1 || fds[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
				t.Fatalf("Run returned before exact descendant termination: n=%d events=%#x err=%v", n, fds[0].Revents, err)
			}
			drain, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if err := m.Shutdown(drain); err != nil {
				t.Fatal(err)
			}
		})
	}
}
