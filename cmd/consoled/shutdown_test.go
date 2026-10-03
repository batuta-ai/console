//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"golang.org/x/sys/unix"
)

func TestShutdownDaemonChild(t *testing.T) {
	if os.Getenv("CONSOLE_SHUTDOWN_CHILD") != "1" {
		return
	}
	os.Args = []string{"consoled", "-home", os.Getenv("CONSOLE_SHUTDOWN_HOME"), "-listen", "127.0.0.1:0"}
	main()
}

// This exercises the real entrypoint, signal, authenticated client, and catalog.
// It requires native loopback permission; run on the host when sandboxed.
func TestDaemonSIGTERMDrainsOwnedGroupBeforeCatalogClose(t *testing.T) {
	home := t.TempDir()
	foreign := exec.Command("/bin/cat")
	input, err := foreign.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); _ = foreign.Wait() }()
	observed, err := session.InspectProcess(foreign.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkspace(domain.Workspace{ID: "ws", Name: "fixture", Root: home}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMission(domain.Mission{ID: "mission", WorkspaceID: "ws"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSession(domain.Session{ID: "old", Process: "running", PID: foreign.Process.Pid, ProcessIdentity: observed.Identity}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	cmd, done, c := startTestDaemon(t, home)
	readyPath, controlPath := filepath.Join(home, "child-ready"), filepath.Join(home, "child-control")
	for _, path := range []string{readyPath, controlPath} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	control, err := os.OpenFile(controlPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	defer control.WriteString("quit\n")
	childPID := make(chan int, 1)
	childEOF := make(chan error, 1)
	go func() {
		f, err := os.Open(readyPath)
		if err != nil {
			childEOF <- err
			return
		}
		defer f.Close()
		r := bufio.NewReader(f)
		line, err := r.ReadString('\n')
		if err != nil {
			childEOF <- err
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			childEOF <- err
			return
		}
		childPID <- pid
		_, err = r.ReadByte()
		childEOF <- err
	}()
	var started domain.Session
	script := `trap '' HUP; /bin/sh -c 'exec 3>"$1"; printf "%s\n" "$$" >&3; read answer <"$2"' child "$1" "$2" & read answer`
	if err := c.Post("/v0/sessions", map[string]interface{}{"mission_id": "mission", "argv": []string{"/bin/sh", "-c", script, "leader", readyPath, controlPath}, "batuta": false}, &started); err != nil {
		t.Fatal(err)
	}
	select {
	case pid := <-childPID:
		pgid, err := unix.Getpgid(pid)
		if err != nil || pgid != started.PID {
			t.Fatalf("child group %d leader %d: %v", pgid, started.PID, err)
		}
		t.Logf("daemon PID=%d, owned leader=%d child=%d PGID=%d; HUP ignored", cmd.Process.Pid, started.PID, pid, pgid)
	case <-time.After(5 * time.Second):
		t.Fatal("child readiness watchdog")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon shutdown watchdog")
	}
	select {
	case err := <-childEOF:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon left HUP-ignoring child alive")
	}
	st, err = store.Open(filepath.Join(home, "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := st.GetSession(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Process != "exited" || terminal.EndedAt == nil {
		t.Fatalf("catalog closed before exit callback: %+v", terminal)
	}
	recovered, err := st.GetSession("old")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Process != "unknown" || recovered.EndedAt != nil {
		t.Fatalf("recovered row changed: %+v", recovered)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := session.InspectProcess(foreign.Process.Pid)
	if err != nil || after != observed {
		t.Fatalf("foreign catalog process affected: %+v %v", after, err)
	}
	restarted, restartDone, _ := startTestDaemon(t, home)
	if err := restarted.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-restartDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("restart shutdown watchdog")
	}
}

func startTestDaemon(t *testing.T, home string) (*exec.Cmd, <-chan error, *client.Client) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestShutdownDaemonChild$")
	cmd.Env = append(os.Environ(), "CONSOLE_SHUTDOWN_CHILD=1", "CONSOLE_SHUTDOWN_HOME="+home)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	output := make(chan string, 1)
	go func() {
		var lines bytes.Buffer
		scan := bufio.NewScanner(stderr)
		for scan.Scan() {
			line := scan.Text()
			fmt.Fprintln(&lines, line)
			if strings.HasPrefix(line, "consoled listening on ") {
				ready <- strings.TrimPrefix(line, "consoled listening on ")
			}
		}
		output <- lines.String()
	}()
	done := make(chan error, 1)
	go func() {
		text := <-output
		err := cmd.Wait()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, text)
		}
		done <- err
		close(done)
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	var base string
	select {
	case base = <-ready:
	case err := <-done:
		t.Fatalf("daemon exited before listening: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("daemon listen watchdog")
	}
	token, err := os.ReadFile(filepath.Join(home, "auth.secret"))
	if err != nil {
		t.Fatal(err)
	}
	c := client.New(base, string(token))
	if !c.Alive() {
		t.Fatal("listener did not serve authenticated client")
	}
	return cmd, done, c
}

func TestDaemonSIGTERMReportsIncompleteHTTPDrain(t *testing.T) {
	home := t.TempDir()
	cmd, done, c := startTestDaemon(t, home)
	base, err := url.Parse(c.Base)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", base.Host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The server sends Continue only when the authenticated handler starts
	// reading this unfinished body, proving it is active before SIGTERM.
	_, err = fmt.Fprintf(conn, "POST /v0/workspaces HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 10000\r\nExpect: 100-continue\r\n\r\n", base.Host, c.Token)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusContinue {
		t.Fatalf("handler did not await body: HTTP %d", response.StatusCode)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 || !strings.Contains(err.Error(), "daemon shutdown incomplete: context deadline exceeded") {
			t.Fatalf("incomplete HTTP drain did not produce an explicit nonzero exit: %v", err)
		}
		t.Log("active unfinished HTTP request exceeded shared shutdown budget; daemon reported deadline and exited nonzero")
	case <-time.After(12 * time.Second):
		t.Fatal("HTTP shutdown deadline did not bound daemon exit")
	}
}
