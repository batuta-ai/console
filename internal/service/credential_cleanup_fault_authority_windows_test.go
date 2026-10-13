package service

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func TestNativeCleanupFaultFixtureAuthority(t *testing.T) {
	if credentialCleanupLimitedProcess(t, t.Name()) {
		return
	}
	privileges, backup, restorePrivilege := credentialCleanupTokenPrivileges(t)
	if backup || restorePrivilege {
		t.Fatal("fault fixture authority requires actual disabled backup/restore privileges")
	}
	_, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error("owned fixture authority HOME removal", err)
		}
	})
	private, err := consoleruntime.OpenPrivateDirectory(home)
	if err != nil {
		t.Fatal("initial authority HOME not private", err)
	}
	if err := private.Close(); err != nil {
		t.Fatal("initial authority private handle close", err)
	}
	original := credentialCleanupPathState(t, home, "initial")
	children := map[string]credentialCleanupObservation{}
	if err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != home && entry.IsDir() {
			children[path] = credentialCleanupPathState(t, path, "child-initial")
		}
		return nil
	}); err != nil || len(children) == 0 {
		t.Fatal("actual nonempty fixture authority HOME", len(children), err)
	}
	checkChildren := func(phase string) {
		for path, state := range children {
			if credentialCleanupPathState(t, path, phase) != state {
				t.Fatal("fixture changed protected descendant metadata", path, phase)
			}
		}
	}
	apply, restore := credentialCleanupFault(t, home)
	t.Cleanup(restore)
	if err := apply(); err != nil {
		t.Fatal("apply owned cleanup fault", err)
	}
	fault := credentialCleanupPathState(t, home, "fixture-fault")
	wide, err := windows.UTF16PtrFromString(home)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(wide, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("owned fault authority metadata handle", err)
	}
	sd, readErr := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err := errors.Join(readErr, windows.CloseHandle(handle)); err != nil {
		t.Fatal("owned fault authority metadata read/close", err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		t.Fatal("fault fixture did not install actual two-ACE Windows authority", err, fault.aceCount)
	}
	for i := uint32(0); i < 2; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		wantType, wantMask := uint8(windows.ACCESS_DENIED_ACE_TYPE), uint32(windows.FILE_LIST_DIRECTORY|windows.DELETE)
		if i == 1 {
			wantType, wantMask = windows.ACCESS_ALLOWED_ACE_TYPE, windows.STANDARD_RIGHTS_REQUIRED|windows.SYNCHRONIZE|0x1ff
		}
		if ace.Header.AceType != wantType || uint32(ace.Mask) != wantMask || ace.Header.AceFlags != 0 || (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() != original.owner {
			t.Fatal("fault fixture actual noninheriting currentUSER DENY/ALLOW type/mask/SID")
		}
	}
	if fault.owner != original.owner || fault.attributes != original.attributes || fault.volume != original.volume || fault.indexHigh != original.indexHigh || fault.indexLow != original.indexLow || fault.control&windows.SE_DACL_PROTECTED != original.control&windows.SE_DACL_PROTECTED {
		t.Fatal("fault fixture changed owned root owner/type/protection/identity")
	}
	checkChildren("child-fault")
	if _, err := os.ReadDir(home); !errors.Is(err, os.ErrPermission) {
		t.Fatal("actual fixture LIST authority was not denied", err)
	}
	if err := os.RemoveAll(home); !errors.Is(err, os.ErrPermission) {
		t.Fatal("actual fixture cleanup authority was not denied", err)
	}
	if _, err := os.Lstat(home); err != nil || credentialCleanupPathState(t, home, "fixture-after-denial") != fault {
		t.Fatal("actual denial changed fault HOME existence/metadata", err)
	}
	checkChildren("child-after-denial")
	restore()
	restored := credentialCleanupPathState(t, home, "restored")
	if restored.owner != original.owner || restored.aceBytes != original.aceBytes || restored.aceCount != original.aceCount || restored.control&windows.SE_DACL_PROTECTED != original.control&windows.SE_DACL_PROTECTED || restored.attributes != original.attributes || restored.volume != original.volume || restored.indexHigh != original.indexHigh || restored.indexLow != original.indexLow {
		t.Fatal("fixture original ACL/owner/protection/attributes/identity not restored")
	}
	checkChildren("child-restored")
	private, err = consoleruntime.OpenPrivateDirectory(home)
	if err != nil {
		t.Fatal("restored authority HOME not strictly private", err)
	}
	if err := private.Close(); err != nil {
		t.Fatal("restored authority private handle close", err)
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatal("restored authority HOME removal", err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restored authority HOME retained", err)
	}
	after, afterBackup, afterRestore := credentialCleanupTokenPrivileges(t)
	if !bytes.Equal(privileges, after) || backup != afterBackup || restorePrivilege != afterRestore {
		t.Fatal("fixture authority changed current token privileges")
	}
	t.Logf("actual cleanup fixture authority root fault stable, protectedDescendants=%d unchanged, original ACL/identity restored, private handles closed and HOME removed", len(children))
}
