package limits

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SO_TYPE from the Windows Winsock SDK; syscall does not export it.
const importSocketType = 0x1008

func makeImportFIFO(t *testing.T, path string) error {
	t.Helper()
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("AF_UNIX fixture requires an absent endpoint %q: %v", path, err)
	}
	bindPath := path
	if len(bindPath) >= len(syscall.RawSockaddrUnix{}.Path) {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		short, err := os.MkdirTemp(home, ".console-afunix-")
		if err != nil {
			return err
		}
		alias := filepath.Join(short, "p")
		t.Cleanup(func() {
			if err := os.Remove(alias); err != nil && !os.IsNotExist(err) {
				t.Errorf("remove owned AF_UNIX alias: %v", err)
			}
			if err := os.Remove(short); err != nil {
				t.Errorf("remove owned AF_UNIX namespace: %v", err)
			}
			if _, err := os.Lstat(short); !os.IsNotExist(err) {
				t.Errorf("owned AF_UNIX namespace survives cleanup: %v", err)
			}
		})
		bindPath = filepath.Join(alias, filepath.Base(path))
		if len(bindPath) >= len(syscall.RawSockaddrUnix{}.Path) {
			return fmt.Errorf("owned AF_UNIX bind path is still too long: %q (%d UTF-8 bytes)", bindPath, len(bindPath))
		}
		// The alias names the actual fixture parent; the endpoint is created there.
		if err := os.Symlink(filepath.Dir(path), alias); err != nil {
			return err
		}
		parentInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			return err
		}
		aliasInfo, err := os.Stat(alias)
		if err != nil {
			return err
		}
		if !parentInfo.IsDir() || !aliasInfo.IsDir() || !os.SameFile(parentInfo, aliasInfo) {
			return fmt.Errorf("owned AF_UNIX alias %q does not identify fixture parent %q", alias, filepath.Dir(path))
		}
	}
	var data syscall.WSAData
	if err := syscall.WSAStartup(0x202, &data); err != nil {
		return err
	}
	t.Cleanup(func() {
		if err := syscall.WSACleanup(); err != nil {
			t.Errorf("release fixture Winsock reference: %v", err)
		}
	})
	listener, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	// A raw listening socket needs no Go poller, acceptor or background goroutine.
	t.Cleanup(func() {
		if err := syscall.Closesocket(listener); err != nil {
			t.Errorf("close AF_UNIX fixture listener: %v", err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove AF_UNIX fixture endpoint: %v", err)
		}
	})
	if err := syscall.Bind(listener, &syscall.SockaddrUnix{Name: bindPath}); err != nil {
		return err
	}
	if err := syscall.Listen(listener, 1); err != nil {
		return err
	}
	if kind, err := syscall.GetsockoptInt(listener, syscall.SOL_SOCKET, importSocketType); err != nil || kind != syscall.SOCK_STREAM {
		return fmt.Errorf("AF_UNIX fixture stream socket: type=%d err=%v", kind, err)
	}
	physical, err := windowsImportEndpointInfo(path)
	if err != nil {
		return err
	}
	bound, err := windowsImportEndpointInfo(bindPath)
	if err != nil {
		return err
	}
	if !os.SameFile(physical, bound) {
		return fmt.Errorf("AF_UNIX bind path %q does not identify requested endpoint %q", bindPath, path)
	}
	t.Logf("Windows filesystem AF_UNIX stream listener: requested=%q bind=%q; same physical ModeSocket endpoint, IO_REPARSE_TAG_AF_UNIX=%#x; no acceptor or writer; not a FIFO or blocking ReadFile guarantee", path, bindPath, importAFUnixReparseTag)
	return nil
}

// Count kernel File objects, including sockets and pipes. Process-wide handle
// totals also include lazily initialized Go thread events, which are not FDs.
func openDescriptorCount(t *testing.T) int {
	t.Helper()
	reference, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reference.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	// PROCESS_HANDLE_SNAPSHOT_INFORMATION / PROCESS_HANDLE_TABLE_ENTRY_INFO.
	// This test-only NT query fails closed if the platform changes its layout.
	buffer := make([]uintptr, 8192)
	var used uint32
	if err := windows.NtQueryInformationProcess(windows.CurrentProcess(), windows.ProcessHandleInformation,
		unsafe.Pointer(&buffer[0]), uint32(len(buffer))*uint32(unsafe.Sizeof(uintptr(0))), &used); err != nil {
		t.Fatal("process file-handle snapshot", err)
	}
	type entry struct {
		Handle, HandleCount, PointerCount             uintptr
		Access, ObjectTypeIndex, Attributes, Reserved uint32
	}
	header := 2 * unsafe.Sizeof(uintptr(0))
	capacity := uintptr(len(buffer)) * unsafe.Sizeof(uintptr(0))
	if used < uint32(header) || uintptr(used) > capacity || buffer[0] > (uintptr(used)-header)/unsafe.Sizeof(entry{}) {
		t.Fatal("invalid process handle snapshot length", used, buffer[0])
	}
	entries := unsafe.Slice((*entry)(unsafe.Add(unsafe.Pointer(&buffer[0]), header)), int(buffer[0]))
	var fileType uint32
	for _, e := range entries {
		if e.Handle == reference.Fd() {
			fileType = e.ObjectTypeIndex
			break
		}
	}
	if fileType == 0 {
		t.Fatal("reference file absent from process handle snapshot")
	}
	count := -1 // Exclude the reference owned only by this measurement.
	for _, e := range entries {
		if e.ObjectTypeIndex == fileType {
			count++
		}
	}
	return count
}
