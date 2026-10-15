package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
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
	// The acknowledgment keeps the owned process alive until the caller has its
	// process handle. The deadline also bounds its life if the caller fails.
	const helper = `package main
import("encoding/json"; "os"; "path/filepath"; "time")
func main() {
 home := os.Args[2]
 self, err := os.Executable(); if err != nil { panic(err) }
 data, err := json.Marshal(struct { Path string; Args []string; PID int }{self, os.Args[1:], os.Getpid()}); if err != nil { panic(err) }
 if err := os.WriteFile(filepath.Join(home,"receipt.tmp"), data, 0600); err != nil { panic(err) }
 if err := os.Rename(filepath.Join(home,"receipt.tmp"), filepath.Join(home,"receipt.json")); err != nil { panic(err) }
 ticker := time.NewTicker(10*time.Millisecond); defer ticker.Stop()
 deadline := time.NewTimer(5*time.Second); defer deadline.Stop()
 for { select { case <-ticker.C: if _, err := os.Stat(filepath.Join(home,"ack")); err == nil { return }; case <-deadline.C: os.Exit(2) } }
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
	for _, mode := range []string{"sibling", "path"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
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
	const listen = "127.0.0.1:43987"
	if err := startDaemon(consoleruntime.Paths{Home: home, Listen: listen}); err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var receipt struct {
		Path string
		Args []string
		PID  int
	}
	for {
		data, err := os.ReadFile(filepath.Join(home, "receipt.json"))
		if err == nil {
			if err := json.Unmarshal(data, &receipt); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("owned daemon receipt timed out")
		}
	}
	process, err := os.FindProcess(receipt.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if err := os.WriteFile(filepath.Join(home, "ack"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		state, err := process.Wait()
		if err != nil || !state.Success() {
			t.Fatalf("owned daemon completion: %v, %v", state, err)
		}
	} else {
		// startDaemon owns Wait; observe only the PID supplied by our helper.
		for {
			err := process.Signal(syscall.Signal(0))
			if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
				break
			}
			if err != nil {
				t.Fatalf("observe owned daemon completion: %v", err)
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatal("owned daemon did not exit")
			}
		}
	}
	if receipt.Path != os.Getenv("CONSOLE_DISCOVERY_EXPECT") {
		t.Fatalf("executed path = %q, want %q", receipt.Path, os.Getenv("CONSOLE_DISCOVERY_EXPECT"))
	}
	if want := []string{"-home", home, "-listen", listen}; !reflect.DeepEqual(receipt.Args, want) {
		t.Fatalf("arguments = %q, want %q", receipt.Args, want)
	}
}
