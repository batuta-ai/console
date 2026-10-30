package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func task43AccountHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "private identity ç")
	if err := consoleruntime.EnsurePrivateDirectory(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func task43AccountACL(t *testing.T, path string) {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || !windows.EqualSid(owner, u.User.Sid) {
		t.Fatal("foreign identity owner", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("identity inherited broad access", err)
	}
}

func TestTask43WindowsRuntimeEnvironmentCase(t *testing.T) {
	ambient := []string{`Path=C:\system-bin`, `SystemRoot=C:\Windows`, `windir=C:\Windows`, `SystemDrive=C:`, `ComSpec=C:\Windows\System32\cmd.exe`, `USERprofile=foreign`, `TeMp=foreign`, `cOdEx_HoMe=foreign`, `anthropic_api_key=never-copy`, `OPENAI_API_KEY=never-copy`}
	explicit := []string{`home=C:\selected`, `uSeRpRoFiLe=C:\selected`, `AppData=C:\selected\AppData\Roaming`, `LocalAppData=C:\selected\AppData\Local`, `TMP=C:\selected\tmp`, `TEMP=C:\selected\tmp`, `HomeDrive=C:`, `HomePath=\selected`, `CODEX_HOME=C:\selected\.codex`, `CoDex_HoMe=C:\selected\.codex-new`}
	got := map[string]string{}
	for _, entry := range RuntimeEnvironment(ambient, explicit) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != strings.ToUpper(key) {
			t.Fatal("noncanonical Windows key", entry)
		}
		if _, duplicate := got[key]; duplicate {
			t.Fatal("case collision", key)
		}
		got[key] = value
	}
	for key, want := range map[string]string{"PATH": `C:\system-bin`, "SYSTEMROOT": `C:\Windows`, "WINDIR": `C:\Windows`, "SYSTEMDRIVE": "C:", "COMSPEC": `C:\Windows\System32\cmd.exe`, "HOME": `C:\selected`, "USERPROFILE": `C:\selected`, "APPDATA": `C:\selected\AppData\Roaming`, "LOCALAPPDATA": `C:\selected\AppData\Local`, "TEMP": `C:\selected\tmp`, "TMP": `C:\selected\tmp`, "HOMEDRIVE": "C:", "HOMEPATH": `\selected`, "CODEX_HOME": `C:\selected\.codex-new`, "TERM": "xterm-256color"} {
		if got[key] != want {
			t.Fatalf("%s=%q want=%q", key, got[key], want)
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if _, ok := got[key]; ok {
			t.Fatal("ambient credential inherited", key)
		}
	}
	if ExplicitHome([]string{"HoMe=C:\\selected"}) != `C:\selected` {
		t.Fatal("explicit home lookup is case sensitive")
	}
}

func TestTask43WindowsSessionIdentity(t *testing.T) {
	profile := task43AccountHome(t)
	local := filepath.Join(profile, "AppData", "Local")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("USERPROFILE", profile)
	ambientTemp := filepath.Join(t.TempDir(), "ambient temp")
	if err := os.Mkdir(ambientTemp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", ambientTemp)
	t.Setenv("TMP", ambientTemp)
	env, home, err := SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	if !strings.HasPrefix(home, local+string(filepath.Separator)) || strings.HasPrefix(home, ambientTemp+string(filepath.Separator)) {
		t.Fatal("session used ambient temporary authority", home)
	}
	paths := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		paths[key] = value
	}
	for key, want := range map[string]string{"HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "c"), "TEMP": filepath.Join(home, "tmp"), "TMP": filepath.Join(home, "tmp"), "HOMEDRIVE": filepath.VolumeName(home), "HOMEPATH": strings.TrimPrefix(home, filepath.VolumeName(home))} {
		if paths[key] != want {
			t.Fatalf("%s=%q want=%q", key, paths[key], want)
		}
	}
	for key, path := range paths {
		if key == "HOMEDRIVE" || key == "HOMEPATH" {
			continue
		}
		task43AccountACL(t, path)
	}
	for _, name := range []string{filepath.Join(".codex", "auth.json"), filepath.Join(".claude", ".credentials.json")} {
		if _, err := os.Stat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("new home acquired credentials", err)
		}
	}
	entries, err := os.ReadDir(ambientTemp)
	if err != nil || len(entries) != 0 {
		t.Fatal("session mutated ambient temp", err)
	}
}

func TestTask43WindowsSessionRefusesWritableCacheParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "foreign-writable-cache")
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := u.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;0x40;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", parent)
	env, home, err := SessionEnvironment()
	if home != "" {
		os.RemoveAll(home)
	}
	if env != nil || home != "" || err == nil {
		t.Fatal("session accepted foreign cache parent")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("refusal created private artifacts", err)
	}
}

func TestWindowsSessionCanonicalHome(t *testing.T) {
	profile := task43AccountHome(t)
	local := filepath.Join(profile, "AppData", "Local")
	if err := consoleruntime.EnsurePrivateDirectory(local); err != nil {
		t.Fatal(err)
	}
	local, err := filepath.EvalSymlinks(local)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(local, "batuta")
	if err := consoleruntime.EnsurePrivateDirectory(parent); err != nil {
		t.Fatal(err)
	}
	task43AccountACL(t, local)
	task43AccountACL(t, parent)
	constructed := filepath.Join(local, "Batuta")
	canonical, err := filepath.EvalSymlinks(constructed)
	if err != nil || canonical != parent || canonical == constructed {
		t.Fatalf("fixture must resolve the case alias: constructed=%q canonical=%q parent=%q err=%v", constructed, canonical, parent, err)
	}
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("USERPROFILE", profile)
	env, home, err := SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	canonical, err = filepath.EvalSymlinks(home)
	if err != nil || home != canonical {
		t.Errorf("session HOME is not exactly canonical: home=%q canonical=%q err=%v", home, canonical, err)
	}
	if filepath.Dir(home) != parent {
		t.Errorf("session parent=%q want=%q", filepath.Dir(home), parent)
	}
	paths := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		paths[key] = value
	}
	for key, want := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "c"),
		"TEMP": filepath.Join(home, "tmp"), "TMP": filepath.Join(home, "tmp"), "TMPDIR": filepath.Join(home, "tmp"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"CODEX_HOME": filepath.Join(home, ".codex"), "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "CURSOR_CONFIG_DIR": filepath.Join(home, ".cursor"),
	} {
		path := paths[key]
		if path != want {
			t.Errorf("%s=%q want=%q", key, path, want)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || path != canonical {
			t.Errorf("%s is not exactly canonical: path=%q canonical=%q err=%v", key, path, canonical, err)
		}
		task43AccountACL(t, path)
		dir, err := consoleruntime.OpenPrivateDirectory(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := dir.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for key, want := range map[string]string{"HOMEDRIVE": filepath.VolumeName(home), "HOMEPATH": strings.TrimPrefix(home, filepath.VolumeName(home))} {
		if paths[key] != want {
			t.Errorf("%s=%q want=%q", key, paths[key], want)
		}
	}
}
