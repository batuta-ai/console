//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
)

func TestRestrictedHostRequirementChild(t *testing.T) {
	if os.Getenv("CONSOLE_RESTRICTED_HOST_CHILD") != "1" {
		return
	}
	home := os.Getenv("CONSOLE_RESTRICTED_HOST_HOME")
	if err := session.New(filepath.Join(home, "logs")).HostError(); !errors.Is(err, session.ErrOwnedHostUnsupported) {
		t.Fatalf("constructor admitted restricted proc host: %v", err)
	}
	paths, err := runtime.Resolve(home, os.Getenv("CONSOLE_RESTRICTED_HOST_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runDaemon(paths); !errors.Is(err, session.ErrOwnedHostUnsupported) {
		t.Fatalf("daemon reached listener instead of rejecting host: %v", err)
	}
}

func TestDaemonRejectsRestrictedProcBeforeListener(t *testing.T) {
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	proc := filepath.Join(fixture, "proc")
	if err := os.Mkdir(proc, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "mounts"), []byte("proc /proc proc rw,hidepid=2 0 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "../../internal/session/owned_linux.go")
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	original := `procAbsenceObservable("/proc")`
	if strings.Count(string(data), original) != 1 {
		t.Fatal("native proc reader no longer has one default mount")
	}
	pathJSON, _ := json.Marshal(proc)
	redirected := strings.Replace(string(data), original, "procAbsenceObservable("+string(pathJSON)+")", 1)
	replacement := filepath.Join(fixture, "owned_linux.go")
	if err := os.WriteFile(replacement, []byte(redirected), 0600); err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Clean(source): replacement}})
	overlayFile := filepath.Join(fixture, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(fixture, "consoled-host.test")
	// Only the native reader's filesystem path changes. Constructor and startup
	// guards are real code; the installed daemon gains no fixture setting.
	build := exec.Command(filepath.Join(stdruntime.GOROOT(), "bin", "go"), "test", "-c", "-overlay", overlayFile, "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build restricted host fixture: %v\n%s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	child := exec.Command(binary, "-test.run=^TestRestrictedHostRequirementChild$", "-test.v")
	home := filepath.Join(fixture, "home")
	child.Env = append(os.Environ(), "HOME="+home, "BATUTA_SKILLS="+fixture, "CONSOLE_RESTRICTED_HOST_CHILD=1", "CONSOLE_RESTRICTED_HOST_HOME="+home, "CONSOLE_RESTRICTED_HOST_ADDR="+listener.Addr().String())
	// Reaching net.Listen would return address-in-use instead of the required
	// capability error, so the child remains bounded even if the guard regresses.
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("restricted host startup: %v\n%s", err, output)
	}
}
