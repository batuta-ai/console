//go:build windows && console_ordinary_user

package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	skillTokenElevationDefault = 1
	skillTokenElevationLimited = 3
)

func withOrdinarySkillCaller(t *testing.T, operation func()) {
	t.Helper()
	processBefore := skillLinkTokenPrivileges(t, windows.GetCurrentProcessToken())
	actor := openDesktopSkillToken(t)
	defer func() {
		if err := actor.Close(); err != nil {
			t.Error(err)
		}
	}()
	var caller windows.Token
	if err := windows.DuplicateTokenEx(actor, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &caller); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := caller.Close(); err != nil {
			t.Error(err)
		}
	}()
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	member, err := caller.IsMember(admin)
	if err != nil || member {
		t.Fatal("restricted caller still has administrator authority", member, err)
	}
	before := skillLinkTokenPrivileges(t, caller)
	wide, err := windows.UTF16PtrFromString("SeCreateSymbolicLinkPrivilege")
	if err != nil {
		t.Fatal(err)
	}
	var symlink windows.LUID
	if err := windows.LookupPrivilegeValue(nil, wide, &symlink); err != nil {
		t.Fatal(err)
	}
	for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&before[0])).AllPrivileges() {
		if privilege.Luid == symlink {
			t.Fatal("restricted caller retains special symlink privilege")
		}
	}
	runtime.LockOSThread()
	var previous windows.Token
	err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, true, &previous)
	if err != nil && !errors.Is(err, windows.ERROR_NO_TOKEN) {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	if previous != 0 {
		defer func() {
			if err := previous.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	if err := windows.SetThreadToken(nil, caller); err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer func() {
		if err := windows.SetThreadToken(nil, previous); err != nil {
			t.Error("restore owned test thread token", err)
			return
		}
		runtime.UnlockOSThread()
		if !bytes.Equal(processBefore, skillLinkTokenPrivileges(t, windows.GetCurrentProcessToken())) {
			t.Error("parent process privileges changed")
		}
	}()
	operation()
	var after windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &after); err != nil {
		t.Fatal("caller impersonation lost", err)
	}
	defer func() {
		if err := after.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !bytes.Equal(before, skillLinkTokenPrivileges(t, after)) {
		t.Fatal("restricted caller privileges changed")
	}
	member, err = after.IsMember(admin)
	if err != nil || member {
		t.Fatal("restricted caller acquired administrator authority", member, err)
	}
	t.Logf("actual caller nonadmin=%v privileges=%d, no special symlink privilege", !member, (*windows.Tokenprivileges)(unsafe.Pointer(&before[0])).PrivilegeCount)
}

func openDesktopSkillToken(t *testing.T) windows.Token {
	t.Helper()
	var elevation, elevationSize uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevationType, (*byte)(unsafe.Pointer(&elevation)), 4, &elevationSize); err != nil {
		t.Fatal(err)
	}
	if elevation == skillTokenElevationDefault || elevation == skillTokenElevationLimited {
		var source windows.Token
		if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
			t.Fatal(err)
		}
		var probe windows.Token
		if err := windows.DuplicateTokenEx(source, windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &probe); err != nil {
			t.Fatal(errors.Join(err, source.Close()))
		}
		admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
		if err != nil {
			t.Fatal(errors.Join(err, probe.Close(), source.Close()))
		}
		member, memberErr := probe.IsMember(admin)
		if err := errors.Join(memberErr, probe.Close()); err != nil {
			t.Fatal(errors.Join(err, source.Close()))
		}
		if !member {
			t.Logf("actual ordinary actor from current process elevation=%d; source read only", elevation)
			return source
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(snapshot); err != nil {
			t.Error(err)
		}
	}()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	for err := windows.Process32First(snapshot, &entry); ; err = windows.Process32Next(snapshot, &entry) {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			t.Fatal("no same-user desktop explorer token observed")
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "explorer.exe") {
			continue
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			continue
		}
		var source windows.Token
		openErr := windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source)
		closeErr := windows.CloseHandle(process)
		if closeErr != nil {
			if source != 0 {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			}
			t.Fatal(closeErr)
		}
		if openErr != nil {
			continue
		}
		desktopUser, err := source.GetTokenUser()
		if err != nil {
			t.Fatal(errors.Join(err, source.Close()))
		}
		if !windows.EqualSid(user.User.Sid, desktopUser.User.Sid) {
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var caller windows.Token
		if err := windows.DuplicateTokenEx(source, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &caller); err != nil {
			t.Fatal(errors.Join(err, source.Close()))
		}
		defer func() {
			if err := caller.Close(); err != nil {
				t.Error(err)
			}
		}()
		member, err := caller.IsMember(admin)
		if err != nil {
			t.Fatal(errors.Join(err, source.Close()))
		}
		privileges := skillLinkTokenPrivileges(t, caller)
		var elevation, size uint32
		if err := windows.GetTokenInformation(source, windows.TokenElevationType, (*byte)(unsafe.Pointer(&elevation)), 4, &size); err != nil {
			t.Fatal(errors.Join(err, source.Close()))
		}
		t.Logf("actual same-user desktop PID=%d administrator=%v elevationType=%d privilegeCount=%d; read-only owned token copy", entry.ProcessID, member, elevation, (*windows.Tokenprivileges)(unsafe.Pointer(&privileges[0])).PrivilegeCount)
		return source
	}
}

func TestOrdinaryWindowsUserDiscoversSelectedSkills(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			collection, env, native := nativeSkillFixture(t, adapter)
			withOrdinarySkillCaller(t, func() {
				if result, err := prepareSessionSkills(t.TempDir(), true, adapter, env, []string{"custom-selected"}); err != nil || !result.Enabled {
					t.Fatalf("ordinary user cannot prepare selected skills: %+v %v", result, err)
				}
				entries, err := os.ReadDir(native)
				if err != nil || len(entries) != 5 {
					t.Fatal("actual ordinary discovery set differs", entries, err)
				}
				for _, name := range []string{"batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected"} {
					link, target := filepath.Join(native, name), filepath.Join(collection, name)
					encoded, err := windows.UTF16PtrFromString(link)
					if err != nil {
						t.Fatal(err)
					}
					attrs, err := windows.GetFileAttributes(encoded)
					if err != nil || attrs&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT {
						t.Fatal("selected link lacks physical directory reparse attributes", attrs, err)
					}
					readlink, err := os.Readlink(link)
					if err != nil || readlink != target {
						t.Fatal("selected target differs", readlink, target, err)
					}
					linked, linkErr := os.Stat(link)
					source, sourceErr := os.Stat(target)
					if linkErr != nil || sourceErr != nil || !os.SameFile(linked, source) {
						t.Fatal("kernel target identity differs", linkErr, sourceErr)
					}
					got, readErr := os.ReadFile(filepath.Join(link, "SKILL.md"))
					want, sourceErr := os.ReadFile(filepath.Join(target, "SKILL.md"))
					if readErr != nil || sourceErr != nil || !bytes.Equal(got, want) {
						t.Fatal("selected source bytes differ", readErr, sourceErr)
					}
					t.Logf("actual ordinary provider=%s role=%s attrs=%#x Readlink=%q SameFile=true", adapter, name, attrs, readlink)
				}
				if _, err := os.Lstat(filepath.Join(native, "custom-unrelated")); !os.IsNotExist(err) {
					t.Fatal("unselected role installed", err)
				}
			})
		})
	}
}
