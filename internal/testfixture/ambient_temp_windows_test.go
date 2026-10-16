package testfixture_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unsafe"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

type ordinaryTempSnapshot struct {
	volume, indexHigh, indexLow uint32
	owner, descriptor, acl      string
	control                     windows.SECURITY_DESCRIPTOR_CONTROL
}

func inspectOrdinaryTemp(t *testing.T, path, currentSID string) ordinaryTempSnapshot {
	t.Helper()
	info, sd := credentialReadInfo(t, path)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatal("ordinary TEMP is not a native directory", info)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || owner.String() != currentSID {
		t.Fatal("ordinary TEMP is not current-user-owned", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED != 0 {
		t.Fatal("ordinary TEMP must have an unprotected DACL", control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount < 3 {
		t.Fatal("ordinary TEMP must have multiple access rules", err)
	}
	want := map[string]bool{currentSID: false, "S-1-5-18": false, "S-1-5-32-544": false}
	raw := string(unsafe.Slice((*byte)(unsafe.Pointer(acl)), int(unsafe.Sizeof(*acl))))
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		const fullAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
		if _, ok := want[sid]; !ok || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask != fullAccess {
			t.Fatalf("unexpected ordinary TEMP ACE %d: SID=%s header=%+v mask=%#x", i, sid, ace.Header, ace.Mask)
		}
		want[sid] = true
		raw += string(unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize)))
		t.Logf("ordinary TEMP ACE %d: SID=%s flags=%#x mask=%#x", i, sid, ace.Header.AceFlags, ace.Mask)
	}
	for sid, found := range want {
		if !found {
			t.Fatalf("ordinary TEMP missing trustee %s", sid)
		}
	}
	return ordinaryTempSnapshot{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, owner.String(), sd.String(), raw, control}
}

func TestTerminalEnvironmentPreservesOrdinaryUserTEMP(t *testing.T) {
	parent := testfixture.PrivateHome(t)
	root := filepath.Join(parent, "ordinary user TEMP")
	if err := consolert.EnsurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	// Only this new empty leaf is shaped; no existing TEMP or ancestor ACL is changed.
	if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	before := inspectOrdinaryTemp(t, root, sid)
	t.Logf("before selection: identity=%x:%x:%x owner=%s control=%#x descriptor=%s ACL=%x", before.volume, before.indexHigh, before.indexLow, before.owner, before.control, before.descriptor, before.acl)
	t.Cleanup(func() {
		// Cleanup runs even when TerminalEnvironment calls Fatal in the subtest.
		after := inspectOrdinaryTemp(t, root, sid)
		t.Logf("after selection (failed=%t): identity=%x:%x:%x owner=%s control=%#x descriptor=%s ACL=%x", t.Failed(), after.volume, after.indexHigh, after.indexLow, after.owner, after.control, after.descriptor, after.acl)
		if after != before {
			t.Error("environment selection changed ordinary TEMP identity or exact owner/DACL")
		}
	})
	if dir, err := consolert.OpenPrivateDirectory(root); err == nil {
		if err := dir.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("ordinary TEMP unexpectedly satisfies private-directory policy")
	} else if err.Error() != "private Windows DACL must prevent inherited access" {
		t.Fatal("ordinary TEMP failed outside the expected private DACL precondition", err)
	} else {
		t.Log("ordinary TEMP rejected by private-directory policy:", err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	callerPath := filepath.Join(parent, "caller bin") + string(os.PathListSeparator) + filepath.Join(parent, "other bin")
	original := []string{"HOME=" + parent, "PATH=" + callerPath, "LANG=C.UTF-8", "LC_ALL=C", "SYNTHETIC_ENTRY=literal=value"}
	saved := slices.Clone(original)
	t.Setenv("TEMP", root)
	t.Setenv("TMP", root)
	t.Cleanup(func() {
		if !slices.Equal(original, saved) {
			t.Errorf("environment selection mutated caller slice: got=%q want=%q", original, saved)
		}
	})
	// Selection only: this disposable root must never back a launched MSYS process.
	t.Run("select_environment", func(t *testing.T) {
		selected := testfixture.TerminalEnvironment(t, original)
		values := make(map[string]string)
		for _, entry := range selected {
			key, value, ok := strings.Cut(entry, "=")
			if !ok {
				t.Fatalf("invalid selected environment entry %q", entry)
			}
			values[strings.ToUpper(key)] = value
		}
		want := map[string]string{
			"HOME": parent, "PATH": filepath.Join(filepath.Dir(filepath.Dir(git)), "usr", "bin") + string(os.PathListSeparator) + callerPath,
			"LANG": "C.UTF-8", "LC_ALL": "C", "SYNTHETIC_ENTRY": "literal=value", "TEMP": root, "TMP": root,
		}
		for key, expected := range want {
			if actual, ok := values[key]; !ok || actual != expected {
				t.Errorf("selected %s=%q want=%q", key, actual, expected)
			}
		}
	})
}
