//go:build windows

package batuta

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/routing"
)

func windowsPlanPaths() []struct{ name, path string } {
	locations := routing.PlanLocations("alpha")
	return []struct{ name, path string }{
		{"active_slash", ".batuta/plans/alpha.md"},
		{"active_core_native", locations[0]},
		{"legacy_slash", ".batuta/plan-alpha.md"},
		{"legacy_core_native", locations[1]},
		{"done_slash", ".batuta/plans/done/alpha.md"},
		{"done_native", filepath.Join(".batuta", "plans", "done", "alpha.md")},
	}
}

func windowsPlanFixture(t *testing.T, path string) string {
	t.Helper()
	workspace := t.TempDir()
	full := filepath.Join(workspace, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("# Plan — alpha\r\n**Status:** approved\r\n- [ ] 1. Work — data/high\r\n  Accept: verified\r\n")
	if err := os.WriteFile(full, body, 0600); err != nil {
		t.Fatal(err)
	}
	// Cleanup checks the source even when the regression assertion fails.
	t.Cleanup(func() {
		after, err := os.ReadFile(full)
		if err != nil || !bytes.Equal(after, body) {
			t.Errorf("source bytes changed: path=%q body=%q error=%v", path, after, err)
		}
	})
	return workspace
}

func TestWindowsExactPlanContentCatalogPaths(t *testing.T) {
	for _, tc := range windowsPlanPaths() {
		t.Run(tc.name, func(t *testing.T) {
			workspace := windowsPlanFixture(t, tc.path)
			plan, body, err := ExactPlanContent(workspace, tc.path)
			want, readErr := os.ReadFile(filepath.Join(workspace, tc.path))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err != nil || plan.Path != tc.path || plan.Slug != "alpha" || !bytes.Equal(body, want) {
				t.Errorf("ExactPlanContent(%q): plan=%+v bytes=%q error=%v", tc.path, plan, body, err)
			}
		})
	}
}

func TestWindowsExactPlanContentRejectsInvalidPaths(t *testing.T) {
	workspace := windowsPlanFixture(t, ".batuta/plans/alpha.md")
	for _, tc := range []struct{ name, path string }{
		{"empty", ""},
		{"bare_slug", "alpha"},
		{"bare_filename", "alpha.md"},
		{"missing_extension", ".batuta/plans/alpha"},
		{"wrong_extension", ".batuta/plans/alpha.txt"},
		{"parent_slash", "../.batuta/plans/alpha.md"},
		{"parent_native", `..\.batuta\plans\alpha.md`},
		{"traversal_slash", ".batuta/plans/../plans/alpha.md"},
		{"traversal_native", `.batuta\plans\..\plans\alpha.md`},
		{"traversal_mixed", `.batuta/plans/..\plans/alpha.md`},
		{"done_traversal_slash", ".batuta/plans/done/../alpha.md"},
		{"done_traversal_native", `.batuta\plans\done\..\alpha.md`},
		{"legacy_traversal_slash", ".batuta/plans/../plan-alpha.md"},
		{"legacy_traversal_native", `.batuta\plans\..\plan-alpha.md`},
		{"dot_prefix_slash", "./.batuta/plans/alpha.md"},
		{"dot_prefix_native", `.\.batuta\plans\alpha.md`},
		{"dot_component_slash", ".batuta/plans/./alpha.md"},
		{"dot_component_native", `.batuta\plans\.\alpha.md`},
		{"repeated_separator_slash", ".batuta//plans/alpha.md"},
		{"repeated_separator_native", `.batuta\\plans\alpha.md`},
		{"absolute_native", filepath.Join(workspace, ".batuta", "plans", "alpha.md")},
		{"absolute_slash", filepath.ToSlash(filepath.Join(workspace, ".batuta", "plans", "alpha.md"))},
		{"rooted_slash", "/.batuta/plans/alpha.md"},
		{"rooted_native", `\.batuta\plans\alpha.md`},
		{"drive_relative", `C:.batuta\plans\alpha.md`},
		{"unc", `\\server\share\.batuta\plans\alpha.md`},
		{"device_absolute", `\\?\C:\.batuta\plans\alpha.md`},
		{"workspace_lookalike", ".batuta-other/plans/alpha.md"},
		{"directory_lookalike", ".batuta/plans-other/alpha.md"},
		{"done_lookalike", ".batuta/plans/done-other/alpha.md"},
		{"nested_lookalike", ".batuta/plans/nested/alpha.md"},
		{"legacy_lookalike", ".batuta/other-plan-alpha.md"},
		{"trailing_separator", ".batuta/plans/alpha.md/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, body, err := ExactPlanContent(workspace, tc.path)
			if err != domain.ErrInvalidReference {
				t.Errorf("ExactPlanContent(%q): got %v, want ErrInvalidReference before secure read", tc.path, err)
			}
			if body != nil || !reflect.DeepEqual(plan, routing.Plan{}) {
				t.Errorf("invalid reference returned content: plan=%+v body=%q", plan, body)
			}
		})
	}
}

func TestWindowsPlanSafeReaderPreservesBytes(t *testing.T) {
	for _, tc := range windowsPlanPaths() {
		t.Run(tc.name, func(t *testing.T) {
			workspace := windowsPlanFixture(t, tc.path)
			root, err := os.OpenRoot(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			file, err := openSetupFile(root, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			want, err := os.ReadFile(filepath.Join(workspace, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("safe reader lost exact bytes: bytes=%q error=%v", got, err)
			}
		})
	}
}
