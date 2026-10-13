package batuta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func task43Junction(t *testing.T, alias, target string) {
	t.Helper()
	if err := os.Mkdir(alias, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(alias)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16+2*(len(substitute)+len(printName)))
	binary.LittleEndian.PutUint32(buffer[:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:6], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:12], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(buffer[12:14], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(buffer[14:16], uint16(2*(len(printName)-1)))
	for i, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(buffer[16+i*2:18+i*2], value)
	}
	var returned uint32
	if err := windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("junction was not created: %+v %v", info, err)
	}
}

func TestTask43WindowsSetupJunction(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	want := []byte("foreign junction target\r\n")
	if err := os.WriteFile(filepath.Join(target, "profile.md"), want, 0600); err != nil {
		t.Fatal(err)
	}
	task43Junction(t, filepath.Join(dir, ".batuta"), target)
	if got, err := os.ReadFile(filepath.Join(dir, ".batuta", "profile.md")); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("real junction observation: %q %v", got, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := openSetupFile(root, filepath.Join(".batuta", "profile.md"))
	if f != nil {
		f.Close()
	}
	if f != nil || !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("junction was followed: %v %v", f, err)
	}
}

func TestTask43WindowsSetupHeldFileReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORK.md")
	want, replacement := []byte("original approved bytes\r\n"), []byte("replacement bytes\r\n")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := openSetupFile(root, "WORK.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(path, filepath.Join(dir, "old-WORK.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("held source was rebound: %q %v", got, err)
	}
	current, err := readSetupFile(root, "WORK.md")
	if err != nil || !bytes.Equal(current, replacement) {
		t.Fatalf("new observation does not see replacement: %q %v", current, err)
	}
}

func TestTask43WindowsSetupHeldParentJunctionReplacement(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	parent := filepath.Join(dir, ".batuta")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	want, foreign := []byte("held original parent source\r\n"), []byte("foreign parent source\r\n")
	if err := os.WriteFile(filepath.Join(parent, "profile.md"), want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "profile.md"), foreign, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := openSetupFile(root, filepath.Join(".batuta", "profile.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	moved := filepath.Join(dir, "old-parent")
	if err := os.Rename(parent, moved); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("held child did not prevent intermediate directory replacement: %v", err)
	}
	if _, err := os.Stat(moved); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused replacement moved the original parent: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("held intermediate parent was rebound: %q %v", got, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	f = nil
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	task43Junction(t, parent, target)
	other, err := openSetupFile(root, filepath.Join(".batuta", "profile.md"))
	if other != nil {
		other.Close()
	}
	if other != nil || !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("replacement junction was followed: %v %v", other, err)
	}
}

func TestTask43WindowsSetupHardlinkAddedAfterOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORK.md")
	if err := os.WriteFile(path, []byte("original source"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := openSetupFile(root, "WORK.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Link(path, filepath.Join(dir, "alias.md")); err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil || info.NumberOfLinks != 2 {
		t.Fatalf("held object link count: %+v %v", info, err)
	}
	other, err := openSetupFile(root, "WORK.md")
	if other != nil {
		other.Close()
	}
	if other != nil || !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("new read accepted multiply-linked source: %v %v", other, err)
	}
}
