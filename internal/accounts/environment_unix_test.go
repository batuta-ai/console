//go:build !windows

package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSessionEnvironmentCanonicalPrivateHome(t *testing.T) {
	for _, alias := range []bool{false, true} {
		name := "canonical-parent"
		if alias {
			name = "symlink-parent"
		}
		t.Run(name, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(parent)
			if err != nil {
				t.Fatal(err)
			}
			tempParent := parent
			if alias {
				tempParent = filepath.Join(t.TempDir(), "owned-parent-alias")
				if err := os.Symlink(parent, tempParent); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", tempParent)
			homes := make([]string, 0, 2)
			for range 2 {
				env, home, err := SessionEnvironment()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(home); err != nil {
						t.Error(err)
					}
				})
				resolved, err := filepath.EvalSymlinks(home)
				if err != nil {
					t.Fatal(err)
				}
				if resolved != home {
					t.Errorf("published home is not canonical: got %q, resolved %q", home, resolved)
				}
				if filepath.Dir(resolved) != parent {
					t.Fatalf("home outside owned parent: %q", resolved)
				}
				info, err := os.Lstat(home)
				if err != nil {
					t.Fatal(err)
				}
				if !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatalf("home is not a private directory: %v", info.Mode())
				}
				if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
					t.Fatal("home is not owned by current user")
				}
				aliasInfo, err := os.Stat(filepath.Join(tempParent, filepath.Base(home)))
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(info, aliasInfo) {
					t.Fatal("published home changed the allocated inode")
				}
				paths := map[string]string{"HOME": "", "TMPDIR": "tmp", "XDG_CONFIG_HOME": ".config", "XDG_DATA_HOME": ".local/share", "XDG_CACHE_HOME": ".cache", "XDG_STATE_HOME": ".local/state", "CODEX_HOME": ".codex", "CLAUDE_CONFIG_DIR": ".claude", "CURSOR_CONFIG_DIR": ".cursor"}
				for _, entry := range env {
					key, value, _ := strings.Cut(entry, "=")
					suffix, ok := paths[key]
					if !ok {
						t.Fatalf("unexpected identity key %q", key)
					}
					if want := filepath.Join(resolved, suffix); value != want {
						t.Errorf("%s = %q, want %q", key, value, want)
					}
					pathInfo, err := os.Stat(value)
					if err != nil {
						t.Fatal(err)
					}
					if !pathInfo.IsDir() || pathInfo.Mode().Perm() != 0700 {
						t.Errorf("%s is not private: %v", key, pathInfo.Mode())
					}
					delete(paths, key)
				}
				if len(paths) != 0 {
					t.Errorf("missing identity paths: %v", paths)
				}
				homes = append(homes, home)
			}
			first, err := os.Stat(homes[0])
			if err != nil {
				t.Fatal(err)
			}
			second, err := os.Stat(homes[1])
			if err != nil {
				t.Fatal(err)
			}
			if homes[0] == homes[1] || os.SameFile(first, second) {
				t.Fatal("session homes are not unique")
			}
			for _, home := range homes {
				if err := os.RemoveAll(home); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(home); !os.IsNotExist(err) {
					t.Fatalf("home cleanup failed: %v", err)
				}
			}
			after, err := os.Stat(parent)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("existing parent changed")
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("owned resources retained: %v", entries)
			}
		})
	}
}
