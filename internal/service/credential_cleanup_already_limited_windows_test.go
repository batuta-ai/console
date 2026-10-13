package service

import "testing"

func TestNativeCleanupRejectionFixtureAlreadyLimitedParent(t *testing.T) {
	if credentialCleanupLimitedProcess(t, t.Name()) {
		return
	}
	_, backup, restore := credentialCleanupTokenPrivileges(t)
	if backup || restore {
		t.Fatal("already-limited fixture proof retains backup/restore privileges")
	}
	t.Run("missing-target", TestNativeCleanupLimitedChildRejectsMissingTarget)
	t.Run("privileged-marker", TestNativeCleanupLimitedChildRejectsPrivilegedMarker)
	t.Log("actual already-limited parent exercises in-process no-delegation paths; enabled-privilege rejection coverage belongs to the separate privileged-parent cases")
}
