//go:build windows

package service

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSkillDirectoryLinkExactTarget(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			_, env, native := nativeSkillFixture(t, adapter)
			collection := filepath.Join(t.TempDir(), "Selected skills ação 日本 🚀")
			installedSkillFixture(t, collection, "batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected", "custom-unrelated")
			t.Setenv("BATUTA_SKILLS", collection)
			for range 2 {
				if _, err := prepareSessionSkills(t.TempDir(), true, adapter, env, []string{"custom-selected"}); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := os.ReadDir(native)
			if err != nil || len(entries) != 5 {
				t.Fatalf("selected entries=%v err=%v", entries, err)
			}
			for _, name := range []string{"batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected"} {
				requireWindowsSkillDirectoryLink(t, filepath.Join(native, name), filepath.Join(collection, name))
			}
			if _, err := os.Lstat(filepath.Join(native, "custom-unrelated")); !os.IsNotExist(err) {
				t.Fatalf("unselected skill installed: %v", err)
			}
		})
	}
}

func TestWindowsSkillDirectoryLinkHeldParent(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "destination")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	destination, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(base, "foreign")
	if err := os.Mkdir(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "keep"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	heldPath := filepath.Join(base, "held")
	if err := os.Rename(path, heldPath); err != nil {
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("unexpected replacement error: %v", err)
		}
		t.Logf("held root denies replacement: %v", err)
		heldPath = path
	} else {
		if err := os.Rename(foreign, path); err != nil {
			t.Fatal(err)
		}
		foreign = path
		t.Log("replaced destination pathname while retaining original root")
	}
	current, err := os.Stat(heldPath)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("held destination identity changed: %v", err)
	}
	collection := t.TempDir()
	installedSkillFixture(t, collection, "selected")
	target := filepath.Join(collection, "selected")
	if err := createSkillDirectoryLink(destination, target, "selected"); err != nil {
		t.Fatal(err)
	}
	requireWindowsSkillDirectoryLink(t, filepath.Join(heldPath, "selected"), target)
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("foreign replacement modified: %v %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(foreign, "keep"))
	if err != nil || string(data) != "foreign" {
		t.Fatalf("foreign bytes changed: %q %v", data, err)
	}
}

func TestWindowsSkillDirectoryLinkPreservesConflicts(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			destination, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer destination.Close()
			collection := t.TempDir()
			installedSkillFixture(t, collection, "selected", "foreign")
			target := filepath.Join(collection, "selected")
			foreign := filepath.Join(collection, "foreign")
			conflict := filepath.Join(path, "selected")
			switch kind {
			case "file":
				err = os.WriteFile(conflict, []byte("foreign"), 0600)
			case "directory":
				err = os.Mkdir(conflict, 0700)
			case "symlink":
				err = createSkillDirectoryLink(destination, foreign, "selected")
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(conflict)
			if err != nil {
				t.Fatal(err)
			}
			if err := createSkillDirectoryLink(destination, target, "selected"); !os.IsExist(err) {
				t.Fatalf("existing object must report conflict: %v", err)
			}
			after, err := os.Lstat(conflict)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("existing object changed: %v", err)
			}
			switch kind {
			case "file":
				data, err := os.ReadFile(conflict)
				if err != nil || string(data) != "foreign" {
					t.Fatalf("foreign bytes changed: %q %v", data, err)
				}
			case "directory":
				entries, err := os.ReadDir(conflict)
				if err != nil || len(entries) != 0 {
					t.Fatalf("foreign directory changed: %v %v", entries, err)
				}
			case "symlink":
				requireWindowsSkillDirectoryLink(t, conflict, foreign)
			}
		})
	}
}

func TestWindowsSkillDirectoryLinkRejectsBeforeWrites(t *testing.T) {
	collection := t.TempDir()
	installedSkillFixture(t, collection, "selected")
	valid := filepath.Join(collection, "selected")
	for _, target := range []string{
		"relative", `C:relative`, `\rooted`, `\\server\share\skill`, `\\?\C:\skill`, `\??\C:\skill`,
		`C:\a\..\skill`, `C:\a.`, `C:\a `, `C:\NUL`, `C:\skill:stream`, `C:/skill`,
		"C:\\bad\x00name", "C:\\bad\xffname", `C:\` + strings.Repeat("a", 9000),
		filepath.Join(collection, "missing"), filepath.Join(valid, "SKILL.md"),
	} {
		t.Run("target", func(t *testing.T) {
			home := t.TempDir()
			env := []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex")}
			if err := installNativeSkills("codex", env, map[string]string{"a-valid": valid, "z-invalid": target}); err == nil {
				t.Fatalf("unsupported target accepted: %q", target)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected input wrote destination: %v %v", entries, err)
			}
		})
	}
	for _, name := range []string{"", ".", "..", `..\escape`, `a\b`, `a/b`, `C:\escape`, "NUL", "skill:stream", "skill.", "skill ", "bad\x00name", "bad\xffname"} {
		t.Run("name", func(t *testing.T) {
			home := t.TempDir()
			env := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude")}
			if err := installNativeSkills("claude", env, map[string]string{name: valid}); err == nil {
				t.Fatalf("unsupported name accepted: %q", name)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected name wrote destination: %v %v", entries, err)
			}
		})
	}
}

func TestWindowsSkillDirectoryLinkFailedReparseRemovesOnlyOwnedObject(t *testing.T) {
	path := t.TempDir()
	parent, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := os.WriteFile(filepath.Join(path, "foreign"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	// An undersized buffer forces the real filesystem control operation to fail.
	if err := createWindowsSkillDirectoryLink(parent, "owned", []byte{0}); err == nil {
		t.Fatal("invalid reparse buffer accepted")
	} else {
		t.Logf("reparse failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(path, "owned")); !os.IsNotExist(err) {
		t.Fatalf("owned failed creation remains: %v", err)
	}
	if err := createWindowsSkillDirectoryLink(parent, "foreign", []byte{0}); !os.IsExist(err) {
		t.Fatalf("foreign object must not reach reparse/cleanup: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(path, "foreign"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("foreign object changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 || entries[0].Name() != "foreign" {
		t.Fatalf("unexpected failure residue: %v %v", entries, err)
	}
}

func requireWindowsSkillDirectoryLink(t *testing.T, link, target string) {
	t.Helper()
	encoded, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(encoded)
	if err != nil || attrs&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT {
		t.Fatalf("directory reparse attributes=%#x err=%v", attrs, err)
	}
	got, err := os.Readlink(link)
	if err != nil || got != target {
		t.Fatalf("raw target=%q want=%q err=%v", got, target, err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	want, wantErr := filepath.EvalSymlinks(target)
	if err != nil || wantErr != nil || resolved != want {
		t.Fatalf("resolved=%q want=%q errors=%v/%v", resolved, want, err, wantErr)
	}
	data, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
	expected, sourceErr := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || sourceErr != nil || !bytes.Equal(data, expected) {
		t.Fatalf("selected bytes=%q want=%q errors=%v/%v", data, expected, err, sourceErr)
	}
}
