//go:build windows

package runtime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func task43CurrentSID(t *testing.T) *windows.SID {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid
}

func task43PrivateHome(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private console ç")
	sd, err := windows.SecurityDescriptorFromString("O:" + task43CurrentSID(t).String() + "D:P(A;OICI;FA;;;" + task43CurrentSID(t).String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatal(err)
	}
	return path
}

func task43AssertPrivateACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, task43CurrentSID(t)) {
		t.Fatalf("foreign owner: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL inheritance not protected: control=%x error=%v", control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		t.Fatalf("nonprivate DACL: %v", err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !windows.EqualSid(sid, task43CurrentSID(t)) || uint32(ace.Mask)&windows.FILE_ALL_ACCESS != windows.FILE_ALL_ACCESS {
		t.Fatal("private DACL does not grant only the selected current SID")
	}
}

func TestTask43WindowsRuntimeHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "console space ç")
	paths, err := Resolve(home, "127.0.0.1:0")
	if err != nil || paths.Home != home || paths.Auth != filepath.Join(home, "auth.secret") {
		t.Fatalf("native protected runtime: %+v error=%v", paths, err)
	}
	for _, path := range []string{home, paths.Logs} {
		task43AssertPrivateACL(t, path)
	}
}

func TestTask43WindowsAuthPublication(t *testing.T) {
	home := task43PrivateHome(t)
	path := filepath.Join(home, "auth.secret")
	first, err := LoadOrCreateAuth(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("native token publication: length=%d error=%v", len(first), err)
	}
	for _, name := range []string{"auth.secret", "auth.secret.lock"} {
		task43AssertPrivateACL(t, filepath.Join(home, name))
	}
	second, err := LoadOrCreateAuth(path)
	if err != nil || first != second {
		t.Fatalf("token rotated: error=%v", err)
	}
	legacy := strings.Repeat("synthetic-legacy-", 4)
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreateAuth(path)
	if err != nil || got != legacy {
		t.Fatalf("legacy bytes changed: error=%v", err)
	}
	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadOrCreateAuth(path); got != "" || err == nil {
		t.Fatalf("partial token accepted: length=%d error=%v", len(got), err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "partial" {
		t.Fatal("partial token was replaced", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 2 {
		t.Fatalf("staging files leaked: entries=%d error=%v", len(entries), err)
	}
}

func TestTask43WindowsAuthConcurrentCreators(t *testing.T) {
	path := filepath.Join(task43PrivateHome(t), "auth.secret")
	var wg sync.WaitGroup
	values := make(chan string, 32)
	failures := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			token, err := LoadOrCreateAuth(path)
			values <- token
			failures <- err
		})
	}
	wg.Wait()
	close(values)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := ""
	for got := range values {
		if len(got) != 64 || (want != "" && want != got) {
			t.Fatal("creators observed partial or distinct tokens")
		}
		want = got
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatal("published token differs from creators", err)
	}
}

func TestTask43WindowsDaemonLock(t *testing.T) {
	home := task43PrivateHome(t)
	path := filepath.Join(home, "consoled.lock")
	first := LockInfo{PID: os.Getpid(), Addr: "127.0.0.1:12345", Home: home}
	f, err := WriteLock(path, first)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	task43AssertPrivateACL(t, path)
	info, err := ReadLock(path)
	if err != nil || info != first {
		t.Fatalf("owned lock cannot be discovered: %+v error=%v", info, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteLock(path, LockInfo{PID: 123, Home: "foreign"})
	if second != nil {
		second.Close()
	}
	if second != nil || err == nil {
		t.Fatal("competing daemon obtained owned lock")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("contender changed lock metadata", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := WriteLock(path, first)
	if err != nil {
		t.Fatal("released lock did not become available", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTask43WindowsAuthRejectsAliases(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := task43PrivateHome(t)
			path := filepath.Join(home, "auth.secret")
			target := filepath.Join(t.TempDir(), "foreign-token")
			want := []byte(strings.Repeat("synthetic-only-", 4))
			if err := os.WriteFile(target, want, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "hardlink":
				err = os.Link(target, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if token, err := LoadOrCreateAuth(path); token != "" || err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe auth identity accepted: length=%d error=%v", len(token), err)
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(after, want) {
				t.Fatal("foreign target changed", err)
			}
		})
	}
}
