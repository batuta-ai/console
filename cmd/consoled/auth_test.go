//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/runtime"
)

func TestDaemonTokenAndLockSurviveLosingStartup(t *testing.T) {
	t.Setenv("BATUTA_SKILLS", t.TempDir())
	home := t.TempDir()
	daemon, done, api := startTestDaemon(t, home)
	auth, lock := filepath.Join(home, "auth.secret"), filepath.Join(home, "consoled.lock")
	beforeAuth, err := os.ReadFile(auth)
	if err != nil {
		t.Fatal(err)
	}
	beforeLock, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	authInfo, err := os.Stat(auth)
	if err != nil {
		t.Fatal(err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	loser := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShutdownDaemonChild$")
	loser.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + home, "BATUTA_SKILLS=" + os.Getenv("BATUTA_SKILLS"), "CONSOLE_SHUTDOWN_CHILD=1", "CONSOLE_SHUTDOWN_HOME=" + home}
	output, err := loser.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "daemon already running") {
		t.Fatalf("contender did not lose daemon ownership: %v", err)
	}
	var callers sync.WaitGroup
	for i := 0; i < 12; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			token, err := runtime.LoadOrCreateAuth(auth)
			if err != nil {
				t.Error(err)
				return
			}
			if token != string(beforeAuth) || client.New(api.Base, token).Probe() != client.StatusReady {
				t.Error("stable local token did not authenticate")
			}
		}()
	}
	callers.Wait()
	// Release this fixture's HTTP transport, including speculative connections.
	api.HTTP.CloseIdleConnections()
	for _, entry := range []struct {
		path string
		data []byte
		info os.FileInfo
	}{{auth, beforeAuth, authInfo}, {lock, beforeLock, lockInfo}} {
		data, err := os.ReadFile(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, entry.data) || !os.SameFile(info, entry.info) || info.Mode().Perm() != 0600 {
			t.Error("losing startup replaced token or changed lock metadata")
		}
	}
	if err := daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon shutdown watchdog")
	}
}
