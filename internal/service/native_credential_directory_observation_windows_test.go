package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestNativeCredentialDirectoryMetadataObservation(t *testing.T) {
	for _, test := range []struct{ adapter, mode string }{{"codex", "bound"}, {"claude", "bound"}, {"codex", "unbound"}} {
		t.Run(test.adapter+"-"+test.mode, func(t *testing.T) {
			svc, ws, mission, account, out, _ := isolationFixture(t, test.adapter)
			var sess domain.Session
			var err error
			if test.mode == "bound" {
				authenticateFixtureAccount(t, svc, account, "directory-observation")
				sess, err = svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: test.adapter})
			} else {
				sess, err = svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(test.adapter))}}, nil, ws.Root)
			}
			if err != nil {
				t.Fatal(err)
			}
			record := readCredentialRecord(t, svc, sess, out)
			if !record.PTY {
				t.Fatal("directory observation did not use actual native PTY")
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			observed := 0
			for _, key := range identityKeys {
				path := record.Env[key]
				if path == "" {
					continue
				}
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatal("actual credential identity directory", key, err)
				}
				name, err := windows.UTF16PtrFromString(path)
				if err != nil {
					t.Fatal(err)
				}
				handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
				if err != nil {
					t.Fatal("actual directory metadata handle", key, err)
				}
				var native windows.ByHandleFileInformation
				infoErr := windows.GetFileInformationByHandle(handle, &native)
				sd, sdErr := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
				if err := errors.Join(infoErr, sdErr, windows.CloseHandle(handle)); err != nil {
					t.Fatal(err)
				}
				owner, _, err := sd.Owner()
				if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) || native.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
					t.Fatal("actual identity directory owner/type", key, native, err)
				}
				control, _, controlErr := sd.Control()
				acl, _, aclErr := sd.DACL()
				if err := errors.Join(controlErr, aclErr); err != nil || control&windows.SE_DACL_PROTECTED == 0 || acl == nil || acl.AceCount != 1 {
					t.Fatal("actual identity directory DACL", key, control, err)
				}
				var ace *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(acl, 0, &ace); err != nil {
					t.Fatal(err)
				}
				const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
				if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || !windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user.User.Sid) || uint32(ace.Mask)&fullAccess != fullAccess {
					t.Fatal("actual identity directory sole current-user full access", key)
				}
				t.Logf("actual identity directory key=%s path=%s GoPerm=%04o attrs=%#x currentSID=%s owner=%s control=%#x ACEcount=%d mask=%#x", key, path, info.Mode().Perm(), native.FileAttributes, user.User.Sid.String(), owner.String(), control, acl.AceCount, ace.Mask)
				observed++
			}
			if observed == 0 {
				t.Fatal("no actual child identity directories observed")
			}
			if _, err := svc.Session.Stop(sess.ID); err != nil {
				t.Fatal("directory observation child did not drain", err)
			}
		})
	}
}
