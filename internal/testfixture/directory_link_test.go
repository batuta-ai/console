package testfixture

import (
	"os"
	"path/filepath"
	"testing"
)

func checkDirectoryLinkReadsActualTarget(t *testing.T, create func(string, string) error) {
	t.Helper()
	for _, relative := range []bool{false, true} {
		name := "absolute"
		if relative {
			name = "relative"
		}
		t.Run(name, func(t *testing.T) {
			root := PrivateHome(t)
			target := filepath.Join(root, "target & % ! ç")
			link := filepath.Join(root, "alias & % ! ç")
			if err := MakePrivateDirectory(target, 0700); err != nil {
				t.Fatal(err)
			}
			payload := []byte("actual target sentinel")
			if err := os.WriteFile(filepath.Join(target, "sentinel"), payload, 0600); err != nil {
				t.Fatal(err)
			}
			argument := target
			if relative {
				argument = filepath.Base(target)
			}
			if err := create(argument, link); err != nil {
				t.Fatal("create actual directory alias", err)
			}
			data, err := os.ReadFile(filepath.Join(link, "sentinel"))
			if err != nil || string(data) != string(payload) {
				t.Fatal("directory alias did not reach actual target", string(data), err)
			}
			got, err := ResolvePath(link)
			if err != nil {
				t.Fatal(err)
			}
			want, err := ResolvePath(target)
			if err != nil || got != want {
				t.Fatal("kernel target differs", got, want, err)
			}
		})
	}
}

func checkDirectoryLinkParentRemovalPreservesTarget(t *testing.T, create func(string, string) error) {
	t.Helper()
	targetRoot := PrivateHome(t)
	target := filepath.Join(targetRoot, "target")
	if err := MakePrivateDirectory(target, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(PrivateHome(t), "link parent")
	if err := MakePrivateDirectory(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := create(target, filepath.Join(parent, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep" {
		t.Fatal("removing alias parent altered external target", string(data), err)
	}
}

func checkDirectoryLinkRejectsExistingDestination(t *testing.T, create func(string, string) error) {
	t.Helper()
	root := PrivateHome(t)
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "existing")
	for _, p := range []string{target, link} {
		if err := MakePrivateDirectory(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(link, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := create(target, link); err == nil {
		t.Fatal("replaced existing directory")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep" {
		t.Fatal("changed existing destination", err)
	}
}

func TestDirectoryLinkReadsActualTarget(t *testing.T) {
	checkDirectoryLinkReadsActualTarget(t, DirectoryLink)
}
func TestDirectoryLinkParentRemovalPreservesTarget(t *testing.T) {
	checkDirectoryLinkParentRemovalPreservesTarget(t, DirectoryLink)
}
func TestDirectoryLinkRejectsExistingDestination(t *testing.T) {
	checkDirectoryLinkRejectsExistingDestination(t, DirectoryLink)
}
