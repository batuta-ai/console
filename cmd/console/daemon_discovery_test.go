package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
)

// This exercises discovery and process execution, not daemon HTTP or UI startup.
func TestDaemonDiscovery(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle with spaces")
	unrelated := filepath.Join(root, "unrelated cwd")
	pathDir := filepath.Join(root, "isolated path")
	for _, dir := range []string{bundle, unrelated, pathDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	caller := filepath.Join(bundle, testfixture.ExecutableName("console-test"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caller, data, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "daemon.go")
	// The live control exchange binds ownership before any receipt read.
	const helper = `package main
import("encoding/json"; "net"; "os"; "path/filepath"; "time")
func main() {
 deadline := time.Now().Add(5*time.Second)
 home := os.Args[2]
 c, err := net.DialTimeout("tcp", os.Getenv("CONSOLE_DISCOVERY_CONTROL"), time.Until(deadline)); if err != nil { panic(err) }; defer c.Close()
 if err := c.SetDeadline(deadline); err != nil { panic(err) }
 enc, dec := json.NewEncoder(c), json.NewDecoder(c)
 self, err := os.Executable(); if err != nil { panic(err) }
 receipt := struct { Path string; Args []string; PID int }{self, os.Args[1:], os.Getpid()}
 if err := enc.Encode(receipt); err != nil { panic(err) }
 var command string
 if err := dec.Decode(&command); err != nil { panic(err) }
 if command == "ack" { return }; if command != "bound" { panic(command) }
 data, err := json.Marshal(receipt); if err != nil { panic(err) }
 if err := os.WriteFile(filepath.Join(home,"receipt.tmp"), data, 0600); err != nil { panic(err) }
 if err := os.Rename(filepath.Join(home,"receipt.tmp"), filepath.Join(home,"receipt.json")); err != nil { panic(err) }
 if err := enc.Encode("ready"); err != nil { panic(err) }
 if err := dec.Decode(&command); err != nil { panic(err) }; if command != "ack" { panic(command) }
}
`
	if err := os.WriteFile(source, []byte(helper), 0600); err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(bundle, testfixture.ExecutableName("consoled"))
	build := exec.Command("go", "build", "-o", daemon, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v: %s", err, output)
	}
	pathDaemon := filepath.Join(pathDir, testfixture.ExecutableName("consoled"))
	for _, mode := range []string{"sibling", "read-failure", "path"} {
		t.Run(mode, func(t *testing.T) {
			home, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			childPath := pathDir
			expected := daemon
			if mode == "path" {
				data, err := os.ReadFile(daemon)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(pathDaemon, data, 0700); err != nil {
					t.Fatal(err)
				}
				expected = pathDaemon
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, caller, "-test.run=^TestDaemonDiscoveryCaller$", "-test.v")
			cmd.Dir = unrelated
			cmd.Env = []string{"PATH=" + childPath, "PATHEXT=.EXE", "HOME=" + home, "USERPROFILE=" + home, "CONSOLE_DISCOVERY_HOME=" + home, "CONSOLE_DISCOVERY_EXPECT=" + expected}
			if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
				cmd.Env = append(cmd.Env, "SystemRoot="+systemRoot)
			}
			if mode == "read-failure" {
				cmd.Env = append(cmd.Env, "CONSOLE_DISCOVERY_READ_FAILURE=1")
				output, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(output), "receipt read:") || !strings.Contains(string(output), "owned daemon completed") {
					t.Fatalf("read failure must preserve primary error and complete owned helper: %v\n%s", err, output)
				}
				return
			}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native %s discovery: %v\n%s", mode, err, output)
			}
		})
	}
}

func TestDaemonDiscoveryCaller(t *testing.T) {
	home := os.Getenv("CONSOLE_DISCOVERY_HOME")
	if home == "" {
		return
	}
	if err := discoveryCaller(t, home); err != nil {
		t.Fatal(err)
	}
}

type discoveryReceipt struct {
	Path string
	Args []string
	PID  int
}

func discoveryCaller(t *testing.T, home string) (result error) {
	const listen = "127.0.0.1:43987"
	deadline := time.Now().Add(8 * time.Second)
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := listener.SetDeadline(deadline); err != nil {
		return err
	}
	t.Setenv("CONSOLE_DISCOVERY_CONTROL", listener.Addr().String())
	if err := startDaemon(consoleruntime.Paths{Home: home, Listen: listen}); err != nil {
		return fmt.Errorf("startDaemon: %w", err)
	}
	conn, err := listener.AcceptTCP()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	var identity discoveryReceipt
	if err := dec.Decode(&identity); err != nil {
		return err
	}
	// The helper remains blocked on this connection while its process is opened.
	owned, err := bindDiscoveryProcess(identity.PID, os.Getenv("CONSOLE_DISCOVERY_EXPECT"))
	if err != nil {
		return err
	}
	defer owned.release()
	defer func() {
		ackErr := enc.Encode("ack")
		waitErr := owned.wait(deadline)
		if waitErr == nil {
			t.Logf("owned daemon completed: %d", identity.PID)
		}
		result = errors.Join(result, ackErr, waitErr)
	}()
	if identity.Path != os.Getenv("CONSOLE_DISCOVERY_EXPECT") {
		return fmt.Errorf("bound image = %q", identity.Path)
	}
	if err := enc.Encode("bound"); err != nil {
		return err
	}
	var ready string
	if err := dec.Decode(&ready); err != nil {
		return err
	}
	if ready != "ready" {
		return fmt.Errorf("helper readiness = %q", ready)
	}
	receiptPath := filepath.Join(home, "receipt.json")
	if os.Getenv("CONSOLE_DISCOVERY_READ_FAILURE") != "" {
		receiptPath = home
	}
	data, err := readDiscoveryReceipt(receiptPath, deadline, os.ReadFile)
	if err != nil {
		return fmt.Errorf("receipt read: %w", err)
	}
	var receipt discoveryReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return err
	}
	if receipt.PID != identity.PID {
		return fmt.Errorf("receipt PID = %d, bound PID = %d", receipt.PID, identity.PID)
	}
	if receipt.Path != os.Getenv("CONSOLE_DISCOVERY_EXPECT") {
		return fmt.Errorf("executed path = %q, want %q", receipt.Path, os.Getenv("CONSOLE_DISCOVERY_EXPECT"))
	}
	if want := []string{"-home", home, "-listen", listen}; !reflect.DeepEqual(receipt.Args, want) {
		return fmt.Errorf("arguments = %q, want %q", receipt.Args, want)
	}
	return nil
}

func readDiscoveryReceipt(path string, deadline time.Time, read func(string) ([]byte, error)) ([]byte, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		data, err := read(path)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, os.ErrNotExist) && !discoverySharingViolation(err) {
			return nil, err
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return nil, fmt.Errorf("owned daemon receipt timed out: %w", err)
		}
	}
}

func TestDiscoveryReceiptReadFailure(t *testing.T) {
	path := t.TempDir()
	_, want := os.ReadFile(path)
	if want == nil {
		t.Fatal("directory read unexpectedly succeeded")
	}
	calls := 0
	_, err := readDiscoveryReceipt(path, time.Now().Add(8*time.Second), func(path string) ([]byte, error) {
		calls++
		return os.ReadFile(path)
	})
	if err == nil || err.Error() != want.Error() || calls != 1 {
		t.Fatalf("read error = %v, want %v, calls = %d", err, want, calls)
	}
}
