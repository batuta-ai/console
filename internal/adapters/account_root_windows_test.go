package adapters

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/domain"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

type nativeAdapterAccountRootState struct {
	descriptor                              string
	control                                 windows.SECURITY_DESCRIPTOR_CONTROL
	attributes, volume, indexHigh, indexLow uint32
}

func nativeAdapterAccountRootMetadata(t *testing.T, path string) nativeAdapterAccountRootState {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(handle, &info)
	sd, sdErr := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err := errors.Join(infoErr, sdErr, windows.CloseHandle(handle)); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, ownerErr := sd.Owner()
	control, _, controlErr := sd.Control()
	acl, _, aclErr := sd.DACL()
	if ownerErr != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) || controlErr != nil || control&windows.SE_DACL_PROTECTED == 0 || aclErr != nil || acl == nil || acl.AceCount != 2 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || sd.String() == "" {
		t.Fatal("actual readonly account root owner/DACL/type", ownerErr, controlErr, aclErr)
	}
	for i := uint32(0); i < 2; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 {
			t.Fatal("actual readonly root ACE type/flags")
		}
		const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
		if i == 0 && (!windows.EqualSid(sid, user.User.Sid) || uint32(ace.Mask) != fullAccess) || i == 1 && (!sid.IsWellKnown(windows.WinWorldSid) || uint32(ace.Mask) != windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE) {
			t.Fatal("actual readonly root ACE SID/mask")
		}
		t.Logf("actual adapter account root ACE=%d SID=%s flags=%#x mask=%#x", i, sid.String(), ace.Header.AceFlags, ace.Mask)
	}
	t.Logf("actual adapter account root path=%s owner=%s control=%#x attrs=%#x fileID=%d:%d:%d descriptor=%s", path, owner.String(), control, info.FileAttributes, info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, sd.String())
	return nativeAdapterAccountRootState{descriptor: sd.String(), control: control, attributes: info.FileAttributes, volume: info.VolumeSerialNumber, indexHigh: info.FileIndexHigh, indexLow: info.FileIndexLow}
}

func TestNativeReadonlyAccountRootKeepsPrivateIdentities(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			root := task43AdapterHome(t)
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FRFX;;;WD)")
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			before := nativeAdapterAccountRootMetadata(t, root)
			account := domain.Account{ID: id + "-readonly-root", Adapter: id}
			adapter := Adapter{ID: id}
			env, err := adapter.IsolateEnv(root, account)
			if err != nil {
				t.Fatal("readonly account root rejected", err)
			}
			if !privateIdentityDirectories(root, account) {
				t.Fatal("readonly root lost private identity readiness")
			}
			observed := 0
			for _, entry := range env {
				key, path, ok := strings.Cut(entry, "=")
				if !ok || key == "HOMEDRIVE" || key == "HOMEPATH" {
					continue
				}
				relative, err := filepath.Rel(filepath.Join(root, "accounts", account.ID), path)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
					t.Fatal("private identity escaped selected account", key, path, err)
				}
				private, err := consoleruntime.OpenPrivateDirectory(path)
				if err != nil {
					t.Fatal("readonly root weakened private identity", key, err)
				}
				if err := private.Close(); err != nil {
					t.Fatal(err)
				}
				observed++
			}
			if observed == 0 {
				t.Fatal("no actual private identity directory")
			}
			for _, path := range []string{filepath.Join(root, "accounts"), filepath.Join(root, "accounts", account.ID)} {
				private, err := consoleruntime.OpenPrivateDirectory(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := private.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if after := nativeAdapterAccountRootMetadata(t, root); after != before {
				t.Fatal("account isolation or readiness changed readonly root metadata")
			}
			private, privateErr := consoleruntime.OpenPrivateDirectory(root)
			if private != nil {
				if err := private.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if privateErr == nil {
				t.Fatal("private directory policy adopted a readonly account root")
			}
			t.Logf("actual readonly account root adapter=%s privateDirectoryFields=%d unchangedroot=true", id, observed)
		})
	}
}
