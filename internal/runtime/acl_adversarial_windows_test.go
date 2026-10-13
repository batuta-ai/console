package runtime

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTask43WindowsRuntimeRefusesWritableParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "world-delete-child")
	sid := task43CurrentSID(t).String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;0x40;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(parent, "console")
	if paths, err := Resolve(home, "127.0.0.1:0"); paths != (Paths{}) || err == nil {
		t.Fatalf("runtime accepted a replaceable parent: %+v error=%v", paths, err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused parent gained runtime artifacts: %v", err)
	}
}

func TestTask43WindowsAuthPublicationDoesNotReplace(t *testing.T) {
	home := task43PrivateHome(t)
	path := filepath.Join(home, "auth.secret")
	want, err := LoadOrCreateAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := OpenPrivateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if token, err := createWindowsAuthAt(dir, "auth.secret"); token != "" || err == nil {
		t.Fatalf("publication replaced an existing token: length=%d error=%v", len(token), err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatal("nonreplacement lost original bytes", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 2 {
		t.Fatalf("failed publication leaked staging: entries=%d error=%v", len(entries), err)
	}
}

func TestTask43WindowsPrivateACLKernelDenial(t *testing.T) {
	home := task43PrivateHome(t)
	secret := filepath.Join(home, "auth.secret")
	if _, err := LoadOrCreateAuth(secret); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(t.TempDir(), "read-control")
	if err := os.WriteFile(public, []byte("public control"), 0600); err != nil {
		t.Fatal(err)
	}
	publicSD, err := windows.SecurityDescriptorFromString("D:P(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	publicACL, _, err := publicSD.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(public, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, publicACL, nil); err != nil {
		t.Fatal(err)
	}
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	disabled := windows.SIDAndAttributes{Sid: task43CurrentSID(t)}
	var restricted windows.Token
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	ok, _, callErr := create.Call(uintptr(source), 1, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatal(callErr)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	publicName, err := windows.UTF16PtrFromString(public)
	if err != nil {
		t.Fatal(err)
	}
	secretName, err := windows.UTF16PtrFromString(secret)
	if err != nil {
		t.Fatal(err)
	}
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, impersonation); err != nil {
		t.Fatal(err)
	}
	control, controlErr := windows.CreateFile(publicName, windows.FILE_READ_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	protected, protectedErr := windows.CreateFile(secretName, windows.FILE_READ_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if control != windows.InvalidHandle {
		windows.CloseHandle(control)
	}
	if protected != windows.InvalidHandle {
		windows.CloseHandle(protected)
	}
	if err := windows.RevertToSelf(); err != nil {
		// Never return an impersonating thread to Go's scheduler.
		os.Exit(2)
	}
	if controlErr != nil {
		t.Fatalf("restricted token lacked its public read control: %v", controlErr)
	}
	if protected != windows.InvalidHandle || !errors.Is(protectedErr, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("kernel allowed a token without the current-user grant: handle=%v error=%v", protected, protectedErr)
	}
}
