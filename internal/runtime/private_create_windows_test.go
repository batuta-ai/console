package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateCreateSecurity(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestTask43WindowsPrivateCreateOwnedHandle(t *testing.T) {
	home := task43PrivateHome(t)
	path := filepath.Join(home, "bridge missão α.json")
	file, err := CreatePrivateFile(path)
	if err != nil || file == nil {
		t.Fatalf("new native private file: file=%v err=%v", file, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	task43AssertPrivateACL(t, path)
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		t.Fatalf("created object is not regular: %v %v", before, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatalf("created handle is not single-linked regular: %+v %v", info, err)
	}
	security := privateCreateSecurity(t, path)
	for _, body := range [][]byte{[]byte("long original record with stale suffix"), []byte("new\x00\nα")} {
		if err := file.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if n, err := file.Write(body); err != nil || n != len(body) {
			t.Fatalf("owned handle write: n=%d err=%v", n, err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		reader, err := OpenPrivateFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(body)+1)
		n, readErr := reader.Read(got)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(got[:n], body) {
			t.Fatalf("actual persisted bytes=%x want=%x read=%v close=%v", got[:n], body, readErr, closeErr)
		}
		after, err := file.Stat()
		if err != nil || !os.SameFile(before, after) || privateCreateSecurity(t, path) != security {
			t.Fatalf("owned creation object or descriptor changed: %v", err)
		}
	}
	if err := os.Rename(path, path+".replaced"); err == nil {
		t.Fatal("original creation handle allowed its namespace to be replaced")
	}
	task43AssertPrivateACL(t, path)
}

func TestTask43WindowsPrivateCreateRefusesExisting(t *testing.T) {
	for _, kind := range []string{"regular", "directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := task43PrivateHome(t)
			path := filepath.Join(home, "existing")
			outside := filepath.Join(t.TempDir(), "outside")
			body := []byte("unchanged outside\x00\n")
			if err := os.WriteFile(outside, body, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "regular":
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			security, outsideSecurity := privateCreateSecurity(t, path), privateCreateSecurity(t, outside)
			file, createErr := CreatePrivateFile(path)
			if file != nil {
				file.Close()
			}
			if createErr == nil || file != nil {
				t.Fatalf("existing object adopted: file=%v err=%v", file, createErr)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() || privateCreateSecurity(t, path) != security || privateCreateSecurity(t, outside) != outsideSecurity {
				t.Fatalf("refusal changed an existing object: %v", err)
			}
			got, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("outside file changed: %x %v", got, err)
			}
			if kind == "regular" {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("existing regular bytes changed: %x %v", got, err)
				}
			}
			if kind == "symlink" {
				target, err := os.Readlink(path)
				if err != nil || target != outside {
					t.Fatalf("existing link changed: %q %v", target, err)
				}
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 1 || entries[0].Name() != "existing" {
				t.Fatalf("refusal changed namespace: %v %v", entries, err)
			}
		})
	}
}

func TestTask43WindowsPrivateCreateRefusesUnsafeParent(t *testing.T) {
	for _, kind := range []string{"foreign-acl", "reparse-parent"} {
		t.Run(kind, func(t *testing.T) {
			home := task43PrivateHome(t)
			parent := filepath.Join(home, "parent")
			outside := task43PrivateHome(t)
			if kind == "reparse-parent" {
				if err := os.Symlink(outside, parent); err != nil {
					t.Fatal(err)
				}
			} else {
				sid := task43CurrentSID(t).String()
				sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FR;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				name, err := windows.UTF16PtrFromString(parent)
				if err != nil {
					t.Fatal(err)
				}
				attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
				attributes.Length = uint32(unsafe.Sizeof(attributes))
				if err := windows.CreateDirectory(name, &attributes); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(parent)
			if err != nil {
				t.Fatal(err)
			}
			security, outsideSecurity := privateCreateSecurity(t, parent), privateCreateSecurity(t, outside)
			file, createErr := CreatePrivateFile(filepath.Join(parent, "must-not-exist"))
			if file != nil {
				file.Close()
			}
			if createErr == nil || file != nil {
				t.Fatalf("unsafe parent used: file=%v err=%v", file, createErr)
			}
			after, err := os.Lstat(parent)
			if err != nil || !os.SameFile(before, after) || privateCreateSecurity(t, parent) != security || privateCreateSecurity(t, outside) != outsideSecurity {
				t.Fatalf("unsafe parent was changed: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unsafe parent gained a file: %v %v", entries, err)
			}
		})
	}
}
