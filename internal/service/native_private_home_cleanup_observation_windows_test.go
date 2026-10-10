package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/domain"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

type credentialCleanupObservation struct {
	descriptor, owner, aceBytes             string
	control                                 windows.SECURITY_DESCRIPTOR_CONTROL
	attributes, volume, indexHigh, indexLow uint32
	aceCount                                uint16
}

func credentialCleanupState(t *testing.T, handle windows.Handle, path, phase string) credentialCleanupObservation {
	t.Helper()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, ownerErr := sd.Owner()
	control, _, controlErr := sd.Control()
	acl, _, aclErr := sd.DACL()
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if err := errors.Join(ownerErr, controlErr, aclErr, userErr); err != nil || owner == nil || acl == nil || sd.String() == "" {
		t.Fatal("actual owned cleanup metadata", err)
	}
	if !windows.EqualSid(owner, user.User.Sid) || control&windows.SE_DACL_PROTECTED == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatal("actual owned cleanup owner/protection/type")
	}
	state := credentialCleanupObservation{descriptor: sd.String(), owner: owner.String(), control: control, attributes: info.FileAttributes, volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow, aceCount: acl.AceCount}
	private := phase == "initial" || strings.HasPrefix(phase, "child-")
	if private && acl.AceCount != 1 {
		t.Fatal("actual cleanup initial/child is not single-ACE private")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceSize == 0 {
			t.Fatal("actual owned cleanup empty ACE")
		}
		if private && (ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || uint32(ace.Mask) != windows.STANDARD_RIGHTS_REQUIRED|windows.SYNCHRONIZE|0x1ff || !windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user.User.Sid)) {
			t.Fatal("actual cleanup initial/child is not current-user full access")
		}
		state.aceBytes += fmt.Sprintf("%x", unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize)))
		t.Logf("actual cleanup phase=%s path=%s ACE=%d type=%d SID=%s flags=%#x mask=%#x", phase, path, i, ace.Header.AceType, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String(), ace.Header.AceFlags, ace.Mask)
	}
	t.Logf("actual cleanup phase=%s path=%s owner=%s control=%#x ACEcount=%d attrs=%#x fileID=%d:%d:%d descriptor=%s", phase, path, state.owner, state.control, state.aceCount, state.attributes, state.volume, state.indexHigh, state.indexLow, state.descriptor)
	return state
}

func credentialCleanupPathState(t *testing.T, path, phase string) credentialCleanupObservation {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("owned descendant cleanup metadata", err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error("owned descendant metadata handle close", err)
		}
	}()
	return credentialCleanupState(t, handle, path, phase)
}

func TestNativePrivateHomeCleanupPermissionObservation(t *testing.T) {
	if credentialCleanupLimitedProcess(t, t.Name()) {
		return
	}
	for _, kind := range []string{"old-chmod", "owned-dacl"} {
		t.Run(kind, func(t *testing.T) {
			_, ws, _, _, out, _ := isolationFixture(t, "codex")
			manager := session.New(t.TempDir())
			home := ""
			observedFault := false
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := manager.Shutdown(ctx); err != nil && !(observedFault && errors.Is(err, os.ErrPermission)) {
					t.Error("owned diagnostic manager drain", err)
				}
				if home != "" {
					if err := os.RemoveAll(home); err != nil {
						t.Error("owned diagnostic HOME removal", err)
					}
				}
			})
			sess, err := manager.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName("codex"))}}, nil, ws.Root)
			if err != nil {
				t.Fatal(err)
			}
			record := readCredentialRecord(t, &Service{Session: manager}, sess, out)
			home = record.Env["HOME"]
			if home == "" || !record.PTY {
				t.Fatal("actual unbound HOME/ConPTY not observed")
			}
			name, err := windows.UTF16PtrFromString(home)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			closed := false
			closeRoot := func() {
				if !closed {
					if err := windows.CloseHandle(handle); err != nil {
						t.Fatal("owned cleanup handle close", err)
					}
					closed = true
				}
			}
			t.Cleanup(closeRoot)
			originalSD, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			originalACL, _, err := originalSD.DACL()
			if err != nil || originalACL == nil {
				t.Fatal("original owned cleanup DACL", err)
			}
			original := credentialCleanupState(t, handle, home, "initial")
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
				t.Fatal("actual nonempty unbound HOME", len(children), err)
			}
			checkChildren := func(phase string) {
				for path, state := range children {
					if credentialCleanupPathState(t, path, phase) != state {
						t.Fatal("owned HOME fault changed protected descendant", path, phase)
					}
				}
			}
			checkRootPath := func(state credentialCleanupObservation) {
				lookup, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
				if err != nil {
					t.Fatal("owned cleanup root zero-access identity", err)
				}
				var current windows.ByHandleFileInformation
				if err := errors.Join(windows.GetFileInformationByHandle(lookup, &current), windows.CloseHandle(lookup)); err != nil {
					t.Fatal(err)
				}
				if current.FileAttributes != state.attributes || current.VolumeSerialNumber != state.volume || current.FileIndexHigh != state.indexHigh || current.FileIndexLow != state.indexLow {
					t.Fatal("owned cleanup path no longer names held root")
				}
			}
			modified := false
			restore := func() {
				if !modified {
					return
				}
				if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
					t.Fatal("restore owned HOME DACL", err)
				}
				restored := credentialCleanupState(t, handle, home, "restored")
				if restored.owner != original.owner || restored.aceBytes != original.aceBytes || restored.aceCount != original.aceCount || restored.control&windows.SE_DACL_PROTECTED == 0 || restored.attributes != original.attributes || restored.volume != original.volume || restored.indexHigh != original.indexHigh || restored.indexLow != original.indexLow {
					t.Fatal("owned HOME original ACL/protection/identity not restored")
				}
				checkRootPath(restored)
				checkChildren("child-restored")
				f, err := consoleruntime.OpenPrivateDirectory(home)
				if err != nil {
					t.Fatal("restored owned HOME not private", err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				modified = false
			}
			t.Cleanup(restore)
			if kind == "old-chmod" {
				if err := os.Chmod(home, 0); err != nil {
					t.Fatal(err)
				}
				before := credentialCleanupState(t, handle, home, "old-chmod-before-stop")
				if before.owner != original.owner || before.control != original.control || before.aceBytes != original.aceBytes || before.descriptor != original.descriptor {
					t.Fatal("old Chmod changed DACL authority")
				}
				checkRootPath(before)
				stopped, err := manager.Stop(sess.ID)
				t.Logf("actual cleanup kind=%s HOME=%s children=%d StopErr=%v permission=%t process=%s", kind, home, len(children), err, errors.Is(err, os.ErrPermission), stopped.Process)
				if err != nil || stopped.Process != "exited" {
					t.Fatal("old Chmod native cleanup unexpectedly failed", err, stopped.Process)
				}
				closeRoot()
				if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("old Chmod retained actual HOME", err)
				}
				return
			}
			sd, err := windows.SecurityDescriptorFromString("D:P(D;;0x10001;;;" + original.owner + ")(A;;FA;;;" + original.owner + ")")
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil || acl == nil {
				t.Fatal("owned HOME fault DACL construction", err)
			}
			if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			modified = true
			before := credentialCleanupState(t, handle, home, "fault-before-stop")
			checkRootPath(before)
			actualSD, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			actualACL, _, err := actualSD.DACL()
			if err != nil || actualACL == nil || actualACL.AceCount != 2 {
				t.Fatal("actual owned HOME fault ACE count", err)
			}
			for i := uint32(0); i < 2; i++ {
				var ace *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(actualACL, i, &ace); err != nil {
					t.Fatal(err)
				}
				wantType, wantMask := uint8(windows.ACCESS_DENIED_ACE_TYPE), uint32(windows.FILE_LIST_DIRECTORY|windows.DELETE)
				if i == 1 {
					wantType, wantMask = windows.ACCESS_ALLOWED_ACE_TYPE, windows.STANDARD_RIGHTS_REQUIRED|windows.SYNCHRONIZE|0x1ff
				}
				if ace.Header.AceType != wantType || uint32(ace.Mask) != wantMask || ace.Header.AceFlags != 0 || (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String() != original.owner {
					t.Fatal("actual owned HOME noninheritable DENY/ALLOW SID/mask/type")
				}
			}
			checkChildren("child-after-fault")
			stopped, stopErr := manager.Stop(sess.ID)
			t.Logf("actual cleanup kind=%s HOME=%s children=%d StopErr=%v permission=%t process=%s", kind, home, len(children), stopErr, errors.Is(stopErr, os.ErrPermission), stopped.Process)
			if !errors.Is(stopErr, os.ErrPermission) || stopped.Process != "exited" {
				t.Fatal("native owned HOME cleanup fault not retained", stopErr, stopped.Process)
			}
			observedFault = true
			if _, err := os.Lstat(home); err != nil {
				t.Fatal("native cleanup fault HOME disappeared", err)
			}
			if credentialCleanupState(t, handle, home, "fault-after-stop") != before {
				t.Fatal("Stop changed owned fault metadata")
			}
			checkRootPath(before)
			checkChildren("child-after-stop")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shutdownErr := manager.Shutdown(ctx)
			t.Logf("actual cleanup kind=%s ShutdownErr=%v permission=%t HOME=%s", kind, shutdownErr, errors.Is(shutdownErr, os.ErrPermission), home)
			if !errors.Is(shutdownErr, os.ErrPermission) {
				t.Fatal("native HOME unresolved cleanup cause lost", shutdownErr)
			}
			if _, err := os.Lstat(home); err != nil || credentialCleanupState(t, handle, home, "fault-after-shutdown") != before {
				t.Fatal("Shutdown changed owned fault metadata/existence", err)
			}
			checkRootPath(before)
			checkChildren("child-after-shutdown")
			restore()
			closeRoot()
			if err := os.RemoveAll(home); err != nil {
				t.Fatal("restored owned HOME removal", err)
			}
			if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("restored owned HOME retained", err)
			}
			historical := manager.Shutdown(ctx)
			if !errors.Is(historical, os.ErrPermission) {
				t.Fatal("historical manager cleanup cause lost after manual removal", historical)
			}
			t.Logf("actual cleanup ownedHOME restored and removed; historical manager cause remains permission=%t", errors.Is(historical, os.ErrPermission))
		})
	}
}
