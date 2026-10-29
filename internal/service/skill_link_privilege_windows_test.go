//go:build windows

package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func skillLinkTokenPrivileges(t *testing.T, token windows.Token) []byte {
	t.Helper()
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size); err != windows.ERROR_INSUFFICIENT_BUFFER || size == 0 {
		t.Fatal("token privilege size", err, size)
	}
	data := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &data[0], size, &size); err != nil {
		t.Fatal("token privileges", err)
	}
	return data
}

func TestWindowsSkillLinkRestoresCallerPrivileges(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			collection, env, native := nativeSkillFixture(t, adapter)
			processBefore := skillLinkTokenPrivileges(t, windows.GetCurrentProcessToken())
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			var previous windows.Token
			err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, true, &previous)
			if err != nil && !errors.Is(err, windows.ERROR_NO_TOKEN) {
				t.Fatal(err)
			}
			if previous != 0 {
				defer func() {
					if err := previous.Close(); err != nil {
						t.Error(err)
					}
				}()
			}
			var source windows.Token
			if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			}()
			var caller windows.Token
			if err := windows.DuplicateTokenEx(source, windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &caller); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := caller.Close(); err != nil {
					t.Error(err)
				}
			}()
			wide, err := windows.UTF16PtrFromString("SeCreateSymbolicLinkPrivilege")
			if err != nil {
				t.Fatal(err)
			}
			var luid windows.LUID
			if err := windows.LookupPrivilegeValue(nil, wide, &luid); err != nil {
				t.Fatal(err)
			}
			state := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid}}}
			if err := windows.AdjustTokenPrivileges(caller, false, &state, 0, nil, nil); err != nil {
				t.Fatal("disable only owned caller-token privilege", err)
			}
			before := skillLinkTokenPrivileges(t, caller)
			found := false
			for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&before[0])).AllPrivileges() {
				if privilege.Luid == luid {
					found = true
					if privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
						t.Fatal("owned caller token still has enabled symlink privilege")
					}
				}
			}
			t.Logf("owned caller symlink privilege: present=%t enabled=false", found)
			if err := windows.SetThreadToken(nil, caller); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := windows.SetThreadToken(nil, previous); err != nil {
					t.Error("restore original test thread token", err)
				}
			}()
			_, prepareErr := prepareSessionSkills(t.TempDir(), true, adapter, env, []string{"custom-selected"})
			var afterToken windows.Token
			if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &afterToken); err != nil {
				t.Fatal("caller impersonation lost", err)
			}
			defer func() {
				if err := afterToken.Close(); err != nil {
					t.Error(err)
				}
			}()
			after := skillLinkTokenPrivileges(t, afterToken)
			if !bytes.Equal(before, after) || !bytes.Equal(processBefore, skillLinkTokenPrivileges(t, windows.GetCurrentProcessToken())) {
				t.Fatal("link creation changed caller or process privileges")
			}
			t.Logf("actual caller privilege present=%t enabled=false, caller/process unchanged, prepare=%v", found, prepareErr)
			if prepareErr != nil {
				t.Fatal("selected directory links with unavailable caller privilege", prepareErr)
			}
			for _, name := range []string{"batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected"} {
				requireWindowsSkillDirectoryLink(t, filepath.Join(native, name), filepath.Join(collection, name))
			}
			if _, err := os.Lstat(filepath.Join(native, "custom-unrelated")); !os.IsNotExist(err) {
				t.Fatal("unselected role installed", err)
			}
		})
	}
}

func TestWindowsSkillLinkMissingPrivilegeKeepsCallerAuthority(t *testing.T) {
	collection := t.TempDir()
	installedSkillFixture(t, collection, "selected")
	path := t.TempDir()
	destination, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := destination.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(path, "foreign"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	processBefore := skillLinkTokenPrivileges(t, windows.GetCurrentProcessToken())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var previous windows.Token
	err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, true, &previous)
	if err != nil && !errors.Is(err, windows.ERROR_NO_TOKEN) {
		t.Fatal(err)
	}
	if previous != 0 {
		defer func() {
			if err := previous.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	}()
	var caller windows.Token
	if err := windows.DuplicateTokenEx(source, windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &caller); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := caller.Close(); err != nil {
			t.Error(err)
		}
	}()
	wide, err := windows.UTF16PtrFromString("SeCreateSymbolicLinkPrivilege")
	if err != nil {
		t.Fatal(err)
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, wide, &luid); err != nil {
		t.Fatal(err)
	}
	state := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_REMOVED}}}
	if err := windows.AdjustTokenPrivileges(caller, false, &state, 0, nil, nil); err != nil {
		t.Fatal("remove only private caller-token privilege", err)
	}
	before := skillLinkTokenPrivileges(t, caller)
	for _, privilege := range (*windows.Tokenprivileges)(unsafe.Pointer(&before[0])).AllPrivileges() {
		if privilege.Luid == luid {
			t.Fatal("removed privilege remains in caller token")
		}
	}
	if err := windows.SetThreadToken(nil, caller); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.SetThreadToken(nil, previous); err != nil {
			t.Error(err)
		}
	}()
	err = createSkillDirectoryLink(destination, filepath.Join(collection, "selected"), "selected")
	if err != nil {
		t.Fatalf("directory links must work without the caller's missing symlink privilege: %v", err)
	}
	requireWindowsSkillDirectoryLink(t, filepath.Join(path, "selected"), filepath.Join(collection, "selected"))
	var afterToken windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &afterToken); err != nil {
		t.Fatal("caller impersonation lost", err)
	}
	defer func() {
		if err := afterToken.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !bytes.Equal(before, skillLinkTokenPrivileges(t, afterToken)) || !bytes.Equal(processBefore, skillLinkTokenPrivileges(t, windows.GetCurrentProcessToken())) {
		t.Fatal("missing privilege changed caller or process token")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 || entries[0].Name() != "foreign" || entries[1].Name() != "selected" {
		t.Fatalf("directory link creation changed an unrelated entry: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(path, "foreign"))
	if err != nil || string(data) != "keep" {
		t.Fatal("foreign bytes changed", err)
	}
	t.Log("actual directory link without symlink privilege; caller/process privileges and foreign bytes unchanged")
}
