package service

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func credentialCleanupTokenPrivileges(t *testing.T) ([]byte, bool, bool) {
	t.Helper()
	token := windows.GetCurrentProcessToken()
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size); err != windows.ERROR_INSUFFICIENT_BUFFER || size == 0 {
		t.Fatal("actual process token privilege size", err, size)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buffer[0], size, &size); err != nil {
		t.Fatal("actual process token privileges", err)
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0])).AllPrivileges()
	enabled := map[string]bool{}
	for _, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		wide, err := windows.UTF16PtrFromString(name)
		if err != nil {
			t.Fatal(err)
		}
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, wide, &luid); err != nil {
			t.Fatal(err)
		}
		for _, privilege := range privileges {
			if privilege.Luid == luid {
				enabled[name] = privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0
				t.Logf("actual cleanup token privilege=%s attributes=%#x enabled=%t LUID=%d:%d", name, privilege.Attributes, enabled[name], luid.HighPart, luid.LowPart)
			}
		}
	}
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual cleanup token USER=%s privilegeCount=%d privilegesSHA=%x", user.User.Sid.String(), len(privileges), sha256.Sum256(buffer))
	return buffer, enabled["SeBackupPrivilege"], enabled["SeRestorePrivilege"]
}

func TestNativePrivateHomeCleanupBackupIntentMechanism(t *testing.T) {
	privileges, backup, restore := credentialCleanupTokenPrivileges(t)
	for _, name := range []string{"os/removeall_at.go", "os/root_windows.go", "internal/syscall/windows/at_windows.go"} {
		data, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "src", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal("actual native SDK source", name, err)
		}
		t.Logf("actual cleanup SDK source=%s SHA=%x bytes=%d", name, sha256.Sum256(data), len(data))
	}
	_, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error("owned mechanism HOME removal", err)
		}
	})
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) == 0 {
		t.Fatal("actual nonempty mechanism HOME", err)
	}
	names := func(entries []os.DirEntry) []string {
		result := make([]string, len(entries))
		for i, entry := range entries {
			result[i] = entry.Name()
		}
		slices.Sort(result)
		return result
	}
	initialNames := names(entries)
	wide, err := windows.UTF16PtrFromString(home)
	if err != nil {
		t.Fatal(err)
	}
	held, err := windows.CreateFile(wide, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(held); err != nil {
			t.Error("owned mechanism handle close", err)
		}
	})
	originalSD, err := windows.GetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	originalACL, _, err := originalSD.DACL()
	if err != nil || originalACL == nil {
		t.Fatal("original mechanism ACL", err)
	}
	original := credentialCleanupState(t, held, home, "initial")
	modified := false
	t.Cleanup(func() {
		if !modified {
			return
		}
		if err := windows.SetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
			t.Fatal("restore owned mechanism ACL", err)
		}
		restored := credentialCleanupState(t, held, home, "restored")
		if restored.owner != original.owner || restored.aceBytes != original.aceBytes || restored.aceCount != original.aceCount || restored.control&windows.SE_DACL_PROTECTED == 0 || restored.attributes != original.attributes || restored.volume != original.volume || restored.indexHigh != original.indexHigh || restored.indexLow != original.indexLow {
			t.Fatal("owned mechanism ACL/identity restoration")
		}
		f, err := consoleruntime.OpenPrivateDirectory(home)
		if err != nil {
			t.Fatal("restored mechanism HOME not private", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	sd, err := windows.SecurityDescriptorFromString("D:P(D;;0x10001;;;" + original.owner + ")(A;;FA;;;" + original.owner + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(held, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	modified = true
	before := credentialCleanupState(t, held, home, "mechanism-fault")
	if before.aceCount != 2 {
		t.Fatal("actual mechanism fault ACE count")
	}
	ntName, err := windows.NewNTUnicodeString(`\??\` + home)
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), Attributes: windows.OBJ_CASE_INSENSITIVE, ObjectName: ntName}
	const options = windows.FILE_DIRECTORY_FILE | windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_REPARSE_POINT
	for _, intent := range []uint32{0, windows.FILE_OPEN_FOR_BACKUP_INTENT} {
		var opened windows.Handle
		openErr := windows.NtCreateFile(&opened, windows.FILE_GENERIC_READ, &attributes, &windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options|intent, 0, 0)
		t.Logf("actual cleanup NT read backupIntent=%t access=%#x options=%#x err=%v success=%t tokenBackup=%t tokenRestore=%t", intent != 0, windows.FILE_GENERIC_READ, options|intent, openErr, openErr == nil, backup, restore)
		want := intent != 0 && backup
		if openErr == nil {
			var info windows.ByHandleFileInformation
			infoErr := windows.GetFileInformationByHandle(opened, &info)
			if err := errors.Join(infoErr, windows.CloseHandle(opened)); err != nil {
				t.Fatal(err)
			}
			if info.VolumeSerialNumber != before.volume || info.FileIndexHigh != before.indexHigh || info.FileIndexLow != before.indexLow || info.FileAttributes != before.attributes {
				t.Fatal("NT mechanism open used different object")
			}
		} else if openErr != windows.STATUS_ACCESS_DENIED {
			t.Fatal("unexpected actual NT refusal", openErr)
		}
		if (openErr == nil) != want || credentialCleanupState(t, held, home, "mechanism-after-NT") != before {
			t.Fatal("native backup-intent contrast or unchanged metadata failed", openErr, want)
		}
	}
	entries, readErr := os.ReadDir(home)
	t.Logf("actual cleanup GoReadDir err=%v permission=%t entries=%d tokenBackup=%t tokenRestore=%t", readErr, errors.Is(readErr, os.ErrPermission), len(entries), backup, restore)
	if backup {
		if readErr != nil || !slices.Equal(names(entries), initialNames) {
			t.Fatal("native Go backup-intent enumeration did not preserve entries", readErr)
		}
	} else if !errors.Is(readErr, os.ErrPermission) {
		t.Fatal("native Go directory enumeration did not refuse denied LIST", readErr)
	}
	if credentialCleanupState(t, held, home, "mechanism-after-GoReadDir") != before {
		t.Fatal("GoReadDir changed fault metadata")
	}
	after, afterBackup, afterRestore := credentialCleanupTokenPrivileges(t)
	if !bytes.Equal(privileges, after) || backup != afterBackup || restore != afterRestore {
		t.Fatal("read-only mechanism proof changed token privileges")
	}
}
