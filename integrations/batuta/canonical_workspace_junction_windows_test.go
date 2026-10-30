package batuta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestCanonicalWorkspaceFollowsNativeDirectoryJunction(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "root"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			target := testfixture.PrivateHome(t)
			alias := filepath.Join(testfixture.PrivateHome(t), "workspace alias & ç")
			if err := testfixture.DirectoryJunction(target, alias); err != nil {
				t.Fatal(err)
			}
			input, physical := alias, target
			if nested {
				if err := testfixture.MakePrivateDirectory(filepath.Join(target, "child"), 0700); err != nil {
					t.Fatal(err)
				}
				input = filepath.Join(alias, "child")
				physical = filepath.Join(target, "child")
			}
			want, err := testfixture.ResolvePath(physical)
			if err != nil {
				t.Fatal(err)
			}
			got, err := CanonicalWorkspace(input)
			if err != nil || got != want {
				t.Fatal("workspace alias did not bind physical directory", got, want, err)
			}
		})
	}
}

func TestCanonicalWorkspaceRejectsDanglingNativeDirectoryJunction(t *testing.T) {
	target := filepath.Join(testfixture.PrivateHome(t), "missing")
	alias := filepath.Join(testfixture.PrivateHome(t), "alias")
	if err := testfixture.DirectoryJunction(target, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := CanonicalWorkspace(alias); err == nil || got != "" {
		t.Fatal("accepted dangling directory alias", got, err)
	}
	if _, err := os.Lstat(alias); err != nil {
		t.Fatal("canonicalization changed actual alias", err)
	}
}
