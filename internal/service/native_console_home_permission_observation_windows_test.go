package service

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type credentialConsoleHomeObservation struct {
	descriptor          string
	owner               string
	control             windows.SECURITY_DESCRIPTOR_CONTROL
	attributes, volume  uint32
	indexHigh, indexLow uint32
	aceCount            uint16
}

func credentialObserveConsoleHome(t *testing.T, path, phase string, foreignRead bool) credentialConsoleHomeObservation {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, info := credentialFixtureDirectoryMetadata(t, path)
	owner, _, ownerErr := sd.Owner()
	control, _, controlErr := sd.Control()
	acl, _, aclErr := sd.DACL()
	if ownerErr != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) || controlErr != nil || control&windows.SE_DACL_PROTECTED == 0 || aclErr != nil || acl == nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatal("actual Console root owner/DACL/type", ownerErr, controlErr, aclErr)
	}
	wantCount := uint16(1)
	if foreignRead {
		wantCount = 2
	}
	if acl.AceCount != wantCount {
		t.Fatal("actual Console root ACE count", acl.AceCount)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatal("actual Console root ACE type", i)
		}
		if i == 0 {
			const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
			if !windows.EqualSid(sid, user.User.Sid) || uint32(ace.Mask) != fullAccess || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
				t.Fatal("actual Console root current-user full access")
			}
		} else if !sid.IsWellKnown(windows.WinWorldSid) || uint32(ace.Mask) != windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE || ace.Header.AceFlags != 0 {
			t.Fatal("actual Console root foreign noninheritable read/execute", ace.Mask, ace.Header.AceFlags)
		}
		t.Logf("actual Console root phase=%s ACE=%d SID=%s flags=%#x mask=%#x", phase, i, sid.String(), ace.Header.AceFlags, ace.Mask)
	}
	descriptor := sd.String()
	if descriptor == "" {
		t.Fatal("actual Console root descriptor unavailable")
	}
	goInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	observed := credentialConsoleHomeObservation{descriptor: descriptor, owner: owner.String(), control: control, attributes: info.FileAttributes, volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow, aceCount: acl.AceCount}
	t.Logf("actual Console root phase=%s path=%s GoPerm=%04o attrs=%#x owner=%s control=%#x ACEcount=%d fileID=%d:%d:%d descriptor=%s", phase, path, goInfo.Mode().Perm(), observed.attributes, observed.owner, observed.control, observed.aceCount, observed.volume, observed.indexHigh, observed.indexLow, descriptor)
	return observed
}

func TestNativeConsoleHomePermissionMetadataObservation(t *testing.T) {
	for _, mode := range []string{"old-chmod", "foreign-read-only"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _, account, _, _ := isolationFixture(t, "codex")
			initial := credentialObserveConsoleHome(t, svc.Home, "initial", false)
			if mode == "old-chmod" {
				if err := os.Chmod(svc.Home, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				credentialFixtureDACL(t, svc.Home, "D:P(A;;FA;;;"+initial.owner+")(A;;FRFX;;;WD)", windows.PROTECTED_DACL_SECURITY_INFORMATION)
			}
			before := credentialObserveConsoleHome(t, svc.Home, "before-connect", mode == "foreign-read-only")
			_, connectErr := svc.ConnectAccount(account.ID)
			t.Logf("actual Console root mode=%s chmodMetadataChanged=%t connectErr=%v", mode, initial != before, connectErr)
			after := credentialObserveConsoleHome(t, svc.Home, "after-connect", mode == "foreign-read-only")
			if after != before {
				t.Fatal("ConnectAccount changed existing Console root security/attributes/file identity")
			}
		})
	}
}
