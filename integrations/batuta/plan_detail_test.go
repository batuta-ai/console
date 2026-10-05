package batuta

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const detailSource = "# Plan — Exact\r\n**Goal:** Preserve every byte\r\n**Status:** proposed\r\n\r\n## Tasks\r\n- [ ] 1. First — backend/low\r\n  Scope: one.go\r\n  Accept: passes\r\n- [ ] 2. Second — backend/low\r\n  Depends on: 1\r\n  Accept: also passes\r\n\r\n## Decisions and context\r\nOriginal prose.\r\n"

func TestExactPlanContentLossless(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
		t.Fatal(err)
	}
	path := ".batuta/plans/exact.md"
	original := []byte(detailSource)
	if err := os.WriteFile(filepath.Join(root, path), original, 0600); err != nil {
		t.Fatal(err)
	}
	p, body, err := ExactPlanContent(root, path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, original) || p.Title != "Exact" || p.Goal != "Preserve every byte" || p.Context != "Original prose." || len(p.Tasks) != 2 || len(p.Tasks[1].Dependencies) != 1 || p.Tasks[0].Scope[0] != "one.go" || p.Tasks[0].Accept[0] != "passes" {
		t.Fatalf("lost content: %+v %q", p, body)
	}
	edited := []byte(strings.Replace(detailSource, "Original prose.", "Revised prose.", 1))
	if err := os.WriteFile(filepath.Join(root, path), edited, 0600); err != nil {
		t.Fatal(err)
	}
	next, nextBody, err := ExactPlanContent(root, path)
	if err != nil || next.Set.Digest != p.Set.Digest || bytes.Equal(body, nextBody) {
		t.Fatalf("prose version: %+v %v", next, err)
	}
}

func TestExactPlanContentFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing", "traversal", "symlink", "directory", "oversize", "truncated", "competing"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := ".batuta/plans/exact.md"
			if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
				t.Fatal(err)
			}
			full := filepath.Join(root, path)
			var err error
			switch kind {
			case "missing":
			case "traversal":
				path = ".batuta/plans/../plans/exact.md"
			case "symlink":
				err = os.Symlink(filepath.Join(root, "foreign"), full)
			case "directory":
				err = os.Mkdir(full, 0700)
			case "oversize":
				err = os.WriteFile(full, []byte(detailSource+strings.Repeat("x", 1<<20)), 0600)
			case "truncated":
				err = os.WriteFile(full, []byte("# Plan — Exact\n- [ ] 1. First — backend/low\n"), 0600)
			case "competing":
				err = os.WriteFile(full, []byte(detailSource), 0600)
				if err == nil {
					err = os.WriteFile(filepath.Join(root, ".batuta/plan-exact.md"), []byte(detailSource), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := ExactPlanContent(root, path); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}

func TestExactPlanContentRejectsReadableSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "valid.md")
	if err := os.WriteFile(target, []byte(detailSource), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".batuta/plans/exact.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ExactPlanContent(root, ".batuta/plans/exact.md"); err == nil {
		t.Fatal("readable symlink accepted")
	}
}
