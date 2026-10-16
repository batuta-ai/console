package limits

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IO_REPARSE_TAG_AF_UNIX from the Windows SDK; x/sys does not export it.
const importAFUnixReparseTag = 0x80000023

func windowsImportEndpointInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Type() != os.ModeSocket {
		return nil, fmt.Errorf("physical endpoint %q: mode %v, want ModeSocket", path, info.Mode())
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	var tag struct{ Attributes, ReparseTag uint32 }
	err = windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&tag)), uint32(unsafe.Sizeof(tag)))
	closeErr := windows.CloseHandle(h)
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if tag.Attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || tag.ReparseTag != importAFUnixReparseTag {
		return nil, fmt.Errorf("physical endpoint %q: attributes %#x, tag %#x, want IO_REPARSE_TAG_AF_UNIX", path, tag.Attributes, tag.ReparseTag)
	}
	return info, nil
}

func TestWindowsImportFilesystemEndpoint(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".console-endpoint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	for _, length := range []string{"short", "long"} {
		for _, removal := range []string{"explicit-removal", "fixture-cleanup"} {
			parent := root
			if length == "long" {
				parent = filepath.Join(root, strings.Repeat("long", 28))
				if err := os.MkdirAll(parent, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(parent, "Mixed space é.sock")
			if (len(path) < 108) != (length == "short") {
				t.Fatalf("%s fixture has unsuitable UTF-8 pathname length %d: %q", length, len(path), path)
			}
			t.Run(length+"/"+removal, func(t *testing.T) {
				if err := makeImportFIFO(t, path); err != nil {
					t.Fatal(err)
				}
				physical, err := windowsImportEndpointInfo(path)
				if err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(parent, "endpoint-link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
						t.Error(err)
					}
				})
				linkInfo, err := os.Lstat(link)
				if err != nil || linkInfo.Mode().Type() != os.ModeSymlink {
					t.Fatalf("file symlink: info=%v err=%v", linkInfo, err)
				}
				for _, spelling := range []string{path, link, filepath.Join(parent, "MIXED SPACE É.SOCK")} {
					info, err := statImportFile(spelling)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Type() != os.ModeSocket || !os.SameFile(physical, info) {
						t.Fatalf("%q does not resolve to the original physical socket", spelling)
					}
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				stillPhysical, err := windowsImportEndpointInfo(path)
				if err != nil || !os.SameFile(physical, stillPhysical) {
					t.Fatalf("removing the link changed the endpoint: %v", err)
				}
				if removal == "explicit-removal" {
					if err := os.Symlink(path, link); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(link); !os.IsNotExist(err) {
						t.Fatalf("removed endpoint still reachable through link: %v", err)
					}
				}
			})
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("endpoint survives fixture cleanup: %v", err)
			}
		}
	}
}

func TestWindowsImportStatControls(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("ordinary file"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{regular, directory, filepath.Join(root, "missing")} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			link := target + "-link"
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			want, wantErr := os.Stat(link)
			got, gotErr := statImportFile(link)
			if !reflect.DeepEqual(gotErr, wantErr) {
				t.Fatalf("Stat error changed: got %v, want %v", gotErr, wantErr)
			}
			if wantErr != nil {
				if !os.IsNotExist(wantErr) || got != nil {
					t.Fatalf("dangling symlink: info=%v err=%v", got, gotErr)
				}
				return
			}
			if got.Mode() != want.Mode() || !os.SameFile(got, want) {
				t.Fatalf("ordinary target metadata changed: got %v, want %v", got, want)
			}
		})
	}
}

func TestWindowsImportDescriptorCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter-probe")
	before := openDescriptorCount(t)
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(event); err != nil {
			t.Error(err)
		}
	}()
	if got := openDescriptorCount(t); got != before {
		t.Fatalf("runtime-style event counted as a file descriptor: before=%d after=%d", before, got)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if file != nil {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	if got := openDescriptorCount(t); got != before+1 {
		t.Fatalf("open file not detected: got=%d want=%d", got, before+1)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	file = nil
	if got := openDescriptorCount(t); got != before {
		t.Fatalf("closed file count: got=%d want=%d", got, before)
	}
	var data syscall.WSAData
	if err := syscall.WSAStartup(0x202, &data); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := syscall.WSACleanup(); err != nil {
			t.Error(err)
		}
	}()
	socket, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if socket != syscall.InvalidHandle {
			if err := syscall.Closesocket(socket); err != nil {
				t.Error(err)
			}
		}
	}()
	if got := openDescriptorCount(t); got != before+1 {
		t.Fatalf("open socket not detected: got=%d want=%d", got, before+1)
	}
	if err := syscall.Closesocket(socket); err != nil {
		t.Fatal(err)
	}
	socket = syscall.InvalidHandle
	if got := openDescriptorCount(t); got != before {
		t.Fatalf("closed socket count: got=%d want=%d", got, before)
	}
}
