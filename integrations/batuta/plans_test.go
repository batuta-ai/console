package batuta

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListPlans(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".batuta", "plans")
	if err := os.MkdirAll(filepath.Join(dir, "done"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Plan — Checkout\n\n**Created:** 2026-09-23 · **Status:** proposed\n\n## Tasks\n"
	if err := os.WriteFile(filepath.Join(dir, "checkout.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "done", "old.md"), []byte("**Created:** 2026-09-01 · **Status:** done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ListPlans(root)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].Name != "checkout" || got[0].Status != "proposed" || got[0].Done {
		t.Fatalf("open %+v", got[0])
	}
	if got[1].Name != "old" || !got[1].Done {
		t.Fatalf("archived %+v", got[1])
	}
}
