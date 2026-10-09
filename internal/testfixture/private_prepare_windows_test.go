package testfixture_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestNativeCredentialPreparationPreservesOriginalWrite(t *testing.T) {
	path := filepath.Join(testfixture.PrivateHome(t), "synthetic-auth.json")
	if err := testfixture.PreparePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"synthetic_identity":"fixture-only"}`)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := consolert.OpenPrivateFile(path)
	if err != nil {
		t.Fatal("original credential writing did not retain actual current-SID private file", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil || info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		t.Fatal("credential constructor is not native regular single-link", info, err)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) {
		t.Fatal("credential constructor does not own actual current user", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("credential constructor DACL is not protected", err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		t.Fatal("credential constructor DACL is not exclusive", err)
	}
	if err := testfixture.PreparePrivateFile(path); err != nil {
		t.Fatal("existing safe fixture refused", err)
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || after.String() != sd.String() {
		t.Fatal("preparation changed existing credential security", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(want) {
		t.Fatal("preparation changed original credential bytes", err)
	}
	if err := os.WriteFile(path, []byte("updated-synthetic-only"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err = consolert.OpenPrivateFile(path)
	if err != nil {
		t.Fatal("second original write lost privacy", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCredentialPreparationRefusesUnsafeExistingFile(t *testing.T) {
	for _, kind := range []string{"broad-dacl", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := testfixture.PrivateHome(t)
			target := filepath.Join(home, "synthetic-sentinel")
			f, err := consolert.CreatePrivateFile(target)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.Write([]byte("unchanged-synthetic-only"))
			if err := errors.Join(writeErr, f.Close()); err != nil {
				t.Fatal(err)
			}
			path := target
			switch kind {
			case "broad-dacl":
				user, err := windows.GetCurrentProcessToken().GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				acl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				path = filepath.Join(home, "alias")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				path = filepath.Join(home, "alias")
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			if err := testfixture.PreparePrivateFile(path); err == nil {
				t.Fatal("unsafe existing credential was adopted", kind)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "unchanged-synthetic-only" {
				t.Fatal("unsafe credential bytes changed", err)
			}
			after, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil || after.String() != before.String() {
				t.Fatal("unsafe credential security was repaired", err)
			}
		})
	}
}

func TestNativeCredentialPreparationRefusesMissingParent(t *testing.T) {
	path := filepath.Join(testfixture.PrivateHome(t), "missing", "credential")
	if err := testfixture.PreparePrivateFile(path); err == nil {
		t.Fatal("missing private parent accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused credential target was created", err)
	}
}

func TestNativeCredentialPreparationPreservesRefusedOwnerAndLinks(t *testing.T) {
	for _, kind := range []string{"default-owner", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			home := testfixture.PrivateHome(t)
			target := filepath.Join(home, "synthetic-target")
			if kind == "default-owner" {
				if err := os.WriteFile(target, []byte("synthetic-only-unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				f, err := consolert.CreatePrivateFile(target)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := f.Write([]byte("synthetic-only-unchanged"))
				if err := errors.Join(writeErr, f.Close()); err != nil {
					t.Fatal(err)
				}
			}
			path := target
			if kind != "default-owner" {
				path = filepath.Join(home, "alias")
				var err error
				if kind == "symlink" {
					err = os.Symlink(target, path)
				} else {
					err = os.Link(target, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "default-owner" {
				owner, _, err := before.Owner()
				if err != nil || owner == nil || owner.String() != "S-1-5-32-544" {
					t.Fatal("native QA default-owner fixture is not actual BA-owned", err)
				}
			}
			inspect := func() windows.ByHandleFileInformation {
				f, err := os.Open(target)
				if err != nil {
					t.Fatal(err)
				}
				var info windows.ByHandleFileInformation
				infoErr := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info)
				if err := errors.Join(infoErr, f.Close()); err != nil {
					t.Fatal(err)
				}
				return info
			}
			original := inspect()
			wantLinks := uint32(1)
			if kind == "hardlink" {
				wantLinks = 2
			}
			if original.NumberOfLinks != wantLinks {
				t.Fatal("actual native refusal fixture link count differs", original)
			}
			if err := testfixture.PreparePrivateFile(path); err == nil {
				t.Fatal("unsafe existing owner/alias accepted", kind)
			}
			after := inspect()
			if after.NumberOfLinks != original.NumberOfLinks || after.VolumeSerialNumber != original.VolumeSerialNumber || after.FileIndexHigh != original.FileIndexHigh || after.FileIndexLow != original.FileIndexLow {
				t.Fatal("refusal changed original native object/link count", original, after)
			}
			alias, err := os.Lstat(path)
			if err != nil || (alias.Mode()&os.ModeSymlink != 0) != (kind == "symlink") {
				t.Fatal("refusal removed or changed original alias type", err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "synthetic-only-unchanged" {
				t.Fatal("refusal changed original target bytes", err)
			}
			security, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil || security.String() != before.String() {
				t.Fatal("refusal repaired original target descriptor", err)
			}
		})
	}
}
