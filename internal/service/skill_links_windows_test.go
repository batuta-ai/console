//go:build windows

package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
	"golang.org/x/sys/windows"
)

func TestNativeSkillLinksAreDirectoryReparsePoints(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			collection, env, native := nativeSkillFixture(t, adapter)
			if p, err := prepareSessionSkills(t.TempDir(), true, adapter, env, []string{"custom-selected"}); err != nil || !p.Enabled {
				t.Fatalf("prepare=%+v %v", p, err)
			}
			for _, name := range []string{"batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected"} {
				link := filepath.Join(native, name)
				encoded, err := windows.UTF16PtrFromString(link)
				if err != nil {
					t.Fatal(err)
				}
				attrs, attrErr := windows.GetFileAttributes(encoded)
				target, targetErr := os.Readlink(link)
				expectedTarget := filepath.Join(collection, name)
				resolved, resolveErr := testfixture.ResolvePath(link)
				want, wantErr := testfixture.ResolvePath(filepath.Join(collection, name))
				got, readErr := os.ReadFile(filepath.Join(link, "SKILL.md"))
				expected, sourceErr := os.ReadFile(filepath.Join(collection, name, "SKILL.md"))
				t.Logf("native link=%s attrs=%#x attrErr=%v target=%s targetErr=%v resolved=%s resolveErr=%v want=%s wantErr=%v readErr=%v", link, attrs, attrErr, target, targetErr, resolved, resolveErr, want, wantErr, readErr)
				if attrErr != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
					t.Errorf("selected skill must be a directory reparse point: attrs=%#x err=%v", attrs, attrErr)
				}
				if targetErr != nil || target != expectedTarget || resolveErr != nil || wantErr != nil || resolved != want {
					t.Errorf("selected skill target differs: target=%s expected=%s resolved=%s want=%s errors=%v/%v/%v", target, expectedTarget, resolved, want, targetErr, resolveErr, wantErr)
				}
				if readErr != nil || sourceErr != nil || !bytes.Equal(got, expected) {
					t.Errorf("native selected skill bytes unavailable or changed: read=%v source=%v", readErr, sourceErr)
				}
			}
			if _, err := os.Lstat(filepath.Join(native, "custom-unrelated")); !os.IsNotExist(err) {
				t.Errorf("unselected role reached native account: %v", err)
			}
		})
	}
}
