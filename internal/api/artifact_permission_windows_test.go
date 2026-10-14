package api

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type artifactPermissions struct {
	path     string
	handle   windows.Handle
	original *windows.SECURITY_DESCRIPTOR
	denied   *windows.SECURITY_DESCRIPTOR
	info     windows.ByHandleFileInformation
	contents []byte
}

const artifactSecurity = windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION

func newArtifactPermissions(t *testing.T, path string) *artifactPermissions {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Registered before the caller's restoration cleanup, so restoration runs first.
	t.Cleanup(func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error(err)
		}
	})
	p := &artifactPermissions{path: path, handle: h}
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		t.Fatalf("artifact disk handle: %d %v", kind, err)
	}
	if err := windows.GetFileInformationByHandle(h, &p.info); err != nil {
		t.Fatal(err)
	}
	if p.info.NumberOfLinks != 1 || p.info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		t.Fatal("artifact must be a regular single-link file")
	}
	p.original, err = windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, artifactSecurity)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := p.original.Owner()
	if err != nil {
		t.Fatal(err)
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var size uint32
	err = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER {
		t.Fatalf("default token owner size: %v", err)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size); err != nil {
		t.Fatal(err)
	}
	defaultOwner := *(**windows.SID)(unsafe.Pointer(&buffer[0]))
	if !owner.Equals(user.User.Sid) && !owner.Equals(defaultOwner) {
		t.Fatal("artifact is not owned by current user or default token owner")
	}
	p.contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p.denied, err = windows.SecurityDescriptorFromString("D:P(D;;0x1;;;WD)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *artifactPermissions) unchanged() error {
	current, err := windows.GetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, artifactSecurity)
	if err != nil {
		return err
	}
	owner, _, err := current.Owner()
	if err != nil {
		return err
	}
	originalOwner, _, err := p.original.Owner()
	if err != nil {
		return err
	}
	group, _, err := current.Group()
	if err != nil {
		return err
	}
	originalGroup, _, err := p.original.Group()
	if err != nil {
		return err
	}
	if !owner.Equals(originalOwner) || !group.Equals(originalGroup) {
		return fmt.Errorf("artifact owner or group changed")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(p.handle, &info); err != nil {
		return err
	}
	if info.VolumeSerialNumber != p.info.VolumeSerialNumber || info.FileIndexHigh != p.info.FileIndexHigh || info.FileIndexLow != p.info.FileIndexLow || info.FileAttributes != p.info.FileAttributes || info.NumberOfLinks != 1 || info.FileSizeHigh != p.info.FileSizeHigh || info.FileSizeLow != p.info.FileSizeLow {
		return fmt.Errorf("artifact identity, attributes, links or size changed")
	}
	return nil
}

func artifactACL(sd *windows.SECURITY_DESCRIPTOR) ([]byte, error) {
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	if acl == nil {
		return nil, nil
	}
	size := *(*uint16)(unsafe.Add(unsafe.Pointer(acl), 2))
	return unsafe.Slice((*byte)(unsafe.Pointer(acl)), int(size)), nil
}

func (p *artifactPermissions) chmod(perm os.FileMode) error {
	if err := p.unchanged(); err != nil {
		return err
	}
	var descriptor *windows.SECURITY_DESCRIPTOR
	switch perm {
	case 0000:
		descriptor = p.denied
	case 0600:
		descriptor = p.original
	default:
		return fmt.Errorf("unsupported artifact permission %o", perm)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	security := windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	if control&windows.SE_DACL_PROTECTED != 0 {
		security = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	// Only the DACL is written; its original control bits come from the snapshot.
	setSecurity := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetSecurityObject")
	if err := setSecurity.Find(); err != nil {
		return err
	}
	status, _, _ := setSecurity.Call(uintptr(p.handle), uintptr(security), uintptr(unsafe.Pointer(descriptor)))
	runtime.KeepAlive(descriptor)
	if status != 0 {
		return windows.NTStatus(status)
	}
	current, err := windows.GetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, artifactSecurity)
	if err != nil {
		return err
	}
	wantACL, err := artifactACL(descriptor)
	if err != nil {
		return err
	}
	gotACL, err := artifactACL(current)
	if err != nil {
		return err
	}
	wantControl, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	gotControl, _, err := current.Control()
	if err != nil {
		return err
	}
	if !bytes.Equal(wantACL, gotACL) {
		return fmt.Errorf("artifact DACL mismatch")
	}
	if perm == 0600 {
		if gotControl != wantControl || current.String() != p.original.String() {
			return fmt.Errorf("artifact security descriptor/control not restored exactly")
		}
		contents, err := os.ReadFile(p.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(contents, p.contents) {
			return fmt.Errorf("artifact bytes changed")
		}
	} else if gotControl&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("artifact denial DACL is not protected")
	}
	return p.unchanged()
}
