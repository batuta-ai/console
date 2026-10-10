package service

import (
	"bytes"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNativeCleanupLimitedChildTokenAuthority(t *testing.T) {
	before, backup, restore := credentialCleanupTokenPrivileges(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if backup || restore {
		if !credentialCleanupLimitedProcess(t, t.Name()) {
			t.Fatal("cleanup fixture did not launch an owned limited child despite enabled backup/restore")
		}
		after, afterBackup, afterRestore := credentialCleanupTokenPrivileges(t)
		afterUser, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil || !windows.EqualSid(user.User.Sid, afterUser.User.Sid) || !bytes.Equal(before, after) || backup != afterBackup || restore != afterRestore {
			t.Fatal("limited child fixture changed the parent token", err)
		}
		return
	}
	expected := os.Getenv("BATUTA_CLEANUP_PARENT_USER_SID")
	if expected != "" && user.User.Sid.String() != expected {
		t.Fatal("limited child fixture changed the USER SID", user.User.Sid.String(), expected)
	}
	t.Logf("actual limited cleanup child USER=%s expectedParentUSER=%s tokenBackup=%t tokenRestore=%t", user.User.Sid.String(), expected, backup, restore)
}
