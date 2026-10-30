package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/testfixture"
)

// This test input is provisioned separately so the test token needs no link privilege.
func setupFileLinkWorkspace(t *testing.T) string {
	t.Helper()
	root := os.Getenv("BATUTA_TEST_WINDOWS_FILE_LINK_WORKSPACE")
	if root == "" {
		return ""
	}
	if !filepath.IsAbs(root) {
		t.Fatal("file-link fixture workspace must be absolute")
	}
	physical, err := testfixture.ResolvePath(root)
	if err != nil || !strings.EqualFold(physical, filepath.Clean(root)) {
		t.Fatal("file-link fixture workspace must be physical", err)
	}
	for _, path := range []string{root, filepath.Join(root, ".batuta")} {
		dir, err := consolert.OpenPrivateDirectory(path)
		if err != nil {
			t.Fatal("private file-link fixture directory", err)
		}
		if err := dir.Close(); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, ".batuta", "profile.md")
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("prepared actual file symlink required", err)
	}
	target, err := os.Readlink(link)
	if err != nil || !strings.EqualFold(target, filepath.Join(root, "source-profile")) {
		t.Fatal("prepared file symlink target differs", err)
	}
	resolved, err := testfixture.ResolvePath(link)
	if err != nil || !strings.EqualFold(resolved, target) {
		t.Fatal("prepared file symlink physical target differs", err)
	}
	file, err := consolert.OpenPrivateFile(target)
	if err != nil {
		t.Fatal("private regular file-link target required", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}
