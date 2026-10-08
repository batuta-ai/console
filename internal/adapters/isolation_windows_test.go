package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/domain"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func task43AdapterHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "private console ç")
	if err := consoleruntime.EnsurePrivateDirectory(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestTask43WindowsSelectedAccountHomes(t *testing.T) {
	console := task43AdapterHome(t)
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			account := domain.Account{ID: id + "-selected", Adapter: id}
			adapter := Adapter{ID: id}
			env, err := adapter.IsolateEnv(console, account)
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{}
			for _, entry := range env {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			home := filepath.Join(console, "accounts", account.ID)
			for key, want := range map[string]string{"HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "AppData", "Local"), "TEMP": filepath.Join(home, "tmp"), "TMP": filepath.Join(home, "tmp"), "HOMEDRIVE": filepath.VolumeName(home), "HOMEPATH": strings.TrimPrefix(home, filepath.VolumeName(home))} {
				if values[key] != want {
					t.Fatalf("%s=%q want=%q", key, values[key], want)
				}
			}
			for key, path := range values {
				if key == "HOMEDRIVE" || key == "HOMEPATH" {
					continue
				}
				f, err := consoleruntime.OpenPrivateDirectory(path)
				if err != nil {
					t.Fatalf("unprotected %s: %v", key, err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if !privateIdentityDirectories(console, account) {
				t.Fatal("selected identity not recognized")
			}
			other := domain.Account{ID: account.ID + "-missing", Adapter: id}
			if privateIdentityDirectories(console, other) {
				t.Fatal("missing identity adopted")
			}
		})
	}
}

func TestTask43WindowsReadPrivateInheritedCredential(t *testing.T) {
	console := task43AdapterHome(t)
	dir := filepath.Join(console, "config")
	if err := consoleruntime.EnsurePrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Native file creation inherits the private config DACL; no POSIX chmod.
	sd, err := windows.SecurityDescriptorFromString("O:" + u.User.Sid.String())
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	path := filepath.Join(dir, "synthetic-auth.json")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.CREATE_NEW, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(h), path)
	want := []byte(`{"access_token":"synthetic-only"}`)
	if _, err := f.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := before.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED != 0 {
		t.Fatal("fixture does not use inherited native ACL", err)
	}
	got, err := readPrivateCredential(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("private inherited credential refused: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || after.String() != before.String() {
		t.Fatal("credential read repaired ACL", err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := readPrivateCredential(path); got != nil || err == nil {
		t.Fatal("linked credential adopted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (64<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readPrivateCredential(path); got != nil || err == nil {
		t.Fatal("unbounded credential accepted")
	}
	if _, err := readPrivateCredential(path + ".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing credential: %v", err)
	}
}
