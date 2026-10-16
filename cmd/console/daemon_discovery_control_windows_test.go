package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type discoveryProcess struct{ handle syscall.Handle }

func bindDiscoveryProcess(pid int, expected string) (*discoveryProcess, error) {
	h, err := syscall.OpenProcess(syscall.SYNCHRONIZE|0x1000, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	p := &discoveryProcess{h}
	// Query the opened handle, not the PID or the helper's claimed image.
	query := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	ok, _, queryErr := query.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		p.release()
		return nil, queryErr
	}
	if got := syscall.UTF16ToString(buffer[:size]); got != expected {
		p.release()
		return nil, fmt.Errorf("opened process image = %q, want %q", got, expected)
	}
	event, err := syscall.WaitForSingleObject(h, 0)
	if err != nil || event != syscall.WAIT_TIMEOUT {
		p.release()
		return nil, fmt.Errorf("helper not live at binding: event %d: %v", event, err)
	}
	return p, nil
}
func (p *discoveryProcess) release() { syscall.CloseHandle(p.handle) }
func (p *discoveryProcess) wait(deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	event, err := syscall.WaitForSingleObject(p.handle, uint32((remaining+time.Millisecond-1)/time.Millisecond))
	if err != nil {
		return err
	}
	if event != syscall.WAIT_OBJECT_0 {
		return fmt.Errorf("owned daemon completion event = %d", event)
	}
	var code uint32
	if err := syscall.GetExitCodeProcess(p.handle, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("owned daemon exit code = %d", code)
	}
	return nil
}
func discoverySharingViolation(err error) bool { return errors.Is(err, syscall.Errno(32)) }

func TestDiscoveryReceiptSharing(t *testing.T) {
	deadline := time.Now().Add(8 * time.Second)
	if path := os.Getenv("CONSOLE_DISCOVERY_ORIGINAL_READ_POLICY"); path != "" {
		_, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("original read policy: ERROR_SHARING_VIOLATION=%t: %v", errors.Is(err, syscall.Errno(32)), err)
		}
		t.Fatal("original read policy did not reject the held receipt")
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	want := []byte(`{"Path":"exact image","Args":["-home","spaced home"],"PID":123}`)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_WRITE, syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			syscall.CloseHandle(handle)
		}
	}()
	_, err = os.ReadFile(path)
	if !errors.Is(err, syscall.Errno(32)) {
		t.Fatalf("deny-read handle: got %v, want ERROR_SHARING_VIOLATION", err)
	}
	t.Logf("real ReadFile with deny-read handle: %v", err)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	control := exec.CommandContext(ctx, self, "-test.run=^TestDiscoveryReceiptSharing$", "-test.v")
	control.Env = append(os.Environ(), "CONSOLE_DISCOVERY_ORIGINAL_READ_POLICY="+path)
	output, err := control.CombinedOutput()
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 1 ||
		!strings.Contains(string(output), "original read policy: ERROR_SHARING_VIOLATION=true") {
		t.Fatalf("original reader must fail from sharing contention: %v\n%s", err, output)
	}
	t.Log("original-policy RED: exit=1 cause=ERROR_SHARING_VIOLATION (native ReadFile)")
	calls := 0
	data, err := readDiscoveryReceipt(path, deadline, func(path string) ([]byte, error) {
		calls++
		data, err := os.ReadFile(path)
		if calls == 1 {
			if !errors.Is(err, syscall.Errno(32)) {
				t.Fatalf("held handle read: %v", err)
			}
			if closeErr := syscall.CloseHandle(handle); closeErr != nil {
				t.Fatal(closeErr)
			}
			held = false
		}
		return data, err
	})
	if err != nil || string(data) != string(want) || calls != 2 {
		t.Fatalf("corrected read: %q, %v, calls %d", data, err, calls)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != string(want) {
		t.Fatalf("same handle closed: %q, %v", data, err)
	}
}
