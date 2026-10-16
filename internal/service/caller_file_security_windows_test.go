package service

import (
	"bytes"
	"testing"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

type callerFileSecurity struct {
	descriptor []byte
	control    windows.SECURITY_DESCRIPTOR_CONTROL
	attributes uint32
	volume     uint32
	fileID     [2]uint32
	links      uint32
}

func observeCallerFile(t *testing.T, path string) callerFileSecurity {
	t.Helper()
	file, err := consolert.OpenPrivateFile(path)
	if err != nil {
		t.Fatalf("validate caller file %s: %v", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close caller file %s: %v", path, err)
		}
	}()
	handle := windows.Handle(file.Fd())
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("observe caller file security %s: %v", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("observe caller file control %s: %v", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatalf("observe caller file identity %s: %v", path, err)
	}
	return callerFileSecurity{
		descriptor: bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(descriptor)), descriptor.Length())),
		control:    control,
		attributes: info.FileAttributes,
		volume:     info.VolumeSerialNumber,
		fileID:     [2]uint32{info.FileIndexHigh, info.FileIndexLow},
		links:      info.NumberOfLinks,
	}
}

func snapshotCallerFiles(t *testing.T, credentialPath, settingsPath string) func() {
	t.Helper()
	paths := []string{credentialPath, settingsPath}
	before := make([]callerFileSecurity, len(paths))
	for i, path := range paths {
		before[i] = observeCallerFile(t, path)
	}
	return func() {
		t.Helper()
		for i, path := range paths {
			after := observeCallerFile(t, path)
			want := before[i]
			if !bytes.Equal(after.descriptor, want.descriptor) || after.control != want.control || after.attributes != want.attributes || after.volume != want.volume || after.fileID != want.fileID || after.links != want.links {
				t.Fatalf("caller file security or identity changed: %s", path)
			}
		}
	}
}
