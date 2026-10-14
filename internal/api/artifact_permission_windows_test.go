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
	input, err := descriptor.ToAbsolute()
	if err != nil {
		return err
	}
	// AUTO_INHERITED needs AUTO_INHERIT_REQ on setter input to survive the write.
	// Keep that request bit off the immutable snapshot used for exact readback.
	if control&windows.SE_DACL_AUTO_INHERITED != 0 {
		if err := input.SetControl(windows.SE_DACL_AUTO_INHERIT_REQ, windows.SE_DACL_AUTO_INHERIT_REQ); err != nil {
			return err
		}
	}
	setSecurity := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetSecurityObject")
	if err := setSecurity.Find(); err != nil {
		return err
	}
	status, _, _ := setSecurity.Call(uintptr(p.handle), uintptr(security), uintptr(unsafe.Pointer(input)))
	runtime.KeepAlive(input)
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
			return fmt.Errorf("artifact security descriptor/control not restored exactly: want control=%#04x descriptor=%q; got control=%#04x descriptor=%q", wantControl, p.original.String(), gotControl, current.String())
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

func TestArtifactPermissionsRestoreState(t *testing.T) {
	t.Log("metadata/restored-read regression only; does not qualify ReadFile denial under this actor")
	t.Run("default_file", func(t *testing.T) {
		testArtifactPermissionsRestoreState(t, "", "", "")
	})
	for _, setter := range []string{"SetSecurityInfo", "NtSetSecurityObject"} {
		for _, protection := range []string{"", "P"} {
			for _, inheritance := range []string{"", "AI", "AR", "AIAR"} {
				// SetSecurityInfo accepts an ACL and protection flags, not descriptor control.
				if setter == "SetSecurityInfo" && inheritance != "" {
					continue
				}
				for _, aceFlags := range []string{"", "ID"} {
					name := fmt.Sprintf("%s/protection=%q/control=%q/ace=%q", setter, protection, inheritance, aceFlags)
					t.Run(name, func(t *testing.T) {
						testArtifactPermissionsRestoreState(t, setter, protection+inheritance, aceFlags)
					})
				}
			}
		}
	}
}

func testArtifactPermissionsRestoreState(t *testing.T, setter, controlFlags, aceFlags string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "artifact-restore-")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	contents := []byte("artifact restoration diagnostic\r\n")
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("create owned artifact: write=%v close=%v", writeErr, closeErr)
	}
	// This fixture guards ownership, file type and identity before any initialization write.
	guard := newArtifactPermissions(t, path)
	artifactRestoreReadback(t, guard.handle, "created")
	if setter != "" {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		dacl := "D:" + controlFlags + "(A;;FA;;;" + user.User.Sid.String() + ")"
		if aceFlags != "" {
			// Keep explicit owner access even if the setter replaces inherited ACEs.
			dacl += "(A;" + aceFlags + ";FR;;;" + user.User.Sid.String() + ")"
		}
		requested, err := windows.SecurityDescriptorFromString(dacl)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := requested.Control()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("initialization request: setter=%s control=%#04x descriptor=%q", setter, control, requested.String())
		var security windows.SECURITY_INFORMATION = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
		if control&windows.SE_DACL_PROTECTED != 0 {
			security = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
		var setErr error
		switch setter {
		case "SetSecurityInfo":
			acl, _, err := requested.DACL()
			if err != nil {
				t.Fatal(err)
			}
			setErr = windows.SetSecurityInfo(guard.handle, windows.SE_FILE_OBJECT, security, nil, nil, acl, nil)
		case "NtSetSecurityObject":
			setSecurity := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetSecurityObject")
			if err := setSecurity.Find(); err != nil {
				t.Fatal(err)
			}
			status, _, _ := setSecurity.Call(uintptr(guard.handle), uintptr(security), uintptr(unsafe.Pointer(requested)))
			if status != 0 {
				setErr = windows.NTStatus(status)
			}
		default:
			t.Fatalf("unknown initialization setter %q", setter)
		}
		runtime.KeepAlive(requested)
		artifactRestoreReadback(t, guard.handle, "after initialization")
		if setErr != nil {
			t.Fatalf("initialize owned artifact with %s: %v", setter, setErr)
		}
		if err := guard.unchanged(); err != nil {
			t.Fatal(err)
		}
	}
	// Recapture the actual on-disk state through the real constructor; never edit its snapshot.
	fixture := newArtifactPermissions(t, path)
	artifactRestoreReadback(t, fixture.handle, "before denial")
	want := fixture.original
	wantBytes := artifactRestoreDescriptorBytes(t, want)
	wantControl, _, err := want.Control()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture captured original want: actual SD.Control=%#04x descriptor=%q bytes=%x", wantControl, want.String(), wantBytes)
	t.Cleanup(func() {
		err := fixture.chmod(0600)
		artifactRestoreReadback(t, fixture.handle, "after cleanup restoration")
		if err != nil {
			t.Errorf("cleanup chmod(0600): %v", err)
		}
	})
	denyErr := fixture.chmod(0000)
	artifactRestoreReadback(t, fixture.handle, "after denial")
	if denyErr != nil {
		t.Errorf("chmod(0000): %v", denyErr)
	}
	restoreErr := fixture.chmod(0600)
	got := artifactRestoreReadback(t, fixture.handle, "after restoration (actual got)")
	if restoreErr != nil {
		t.Errorf("chmod(0600): %v", restoreErr)
	}
	gotBytes := artifactRestoreDescriptorBytes(t, got)
	gotControl, _, err := got.Control()
	if err != nil {
		t.Fatal(err)
	}
	if gotControl != wantControl || got.String() != want.String() || !bytes.Equal(gotBytes, wantBytes) {
		t.Errorf("exact original descriptor restoration: want control=%#04x descriptor=%q bytes=%x; got control=%#04x descriptor=%q bytes=%x", wantControl, want.String(), wantBytes, gotControl, got.String(), gotBytes)
	}
	wantACL, err := artifactACL(want)
	if err != nil {
		t.Fatal(err)
	}
	gotACL, err := artifactACL(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotACL, wantACL) {
		t.Errorf("exact original DACL restoration: want=%x got=%x", wantACL, gotACL)
	}
	if err := fixture.unchanged(); err != nil {
		t.Error(err)
	}
	gotContents, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read restored artifact: %v", err)
	}
	if !bytes.Equal(gotContents, contents) {
		t.Errorf("exact original contents restoration: want=%x got=%x", contents, gotContents)
	}
}

func artifactRestoreReadback(t *testing.T, handle windows.Handle, phase string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, artifactSecurity)
	if err != nil {
		t.Fatalf("%s: GetSecurityInfo: %v", phase, err)
	}
	control, revision, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: actual SD.Control=%#04x revision=%d descriptor=%q bytes=%x", phase, control, revision, sd.String(), artifactRestoreDescriptorBytes(t, sd))
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: volume=%#x fileID=%#x:%#x attrs=%#x links=%d size=%d:%d", phase, info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.FileAttributes, info.NumberOfLinks, info.FileSizeHigh, info.FileSizeLow)
	return sd
}

func artifactRestoreDescriptorBytes(t *testing.T, sd *windows.SECURITY_DESCRIPTOR) []byte {
	t.Helper()
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	// GetSecurityInfo returns self-relative descriptors, whose full bytes are contiguous.
	if control&windows.SE_SELF_RELATIVE == 0 {
		t.Fatal("cannot capture bytes of an absolute descriptor")
	}
	data := bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(sd)), int(sd.Length())))
	runtime.KeepAlive(sd)
	return data
}
