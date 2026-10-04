package batuta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/core/loop"
)

func skillFixture(t *testing.T, collection string, names ...string) string {
	t.Helper()
	for _, name := range names {
		dir := filepath.Join(collection, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: fixture\n---\nFixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if name == "batuta" {
			if err := os.Mkdir(filepath.Join(dir, "adapters"), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	return filepath.Join(collection, "batuta")
}

func unsetSkills(t *testing.T) {
	t.Helper()
	t.Setenv("BATUTA_SKILLS", "")
	if err := os.Unsetenv("BATUTA_SKILLS"); err != nil {
		t.Fatal(err)
	}
}

func assertCoreProfile(t *testing.T, workspace, want string) {
	t.Helper()
	p := Prepare(workspace, true)
	want, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.SkillsRoot != want || !filepath.IsAbs(p.SkillsRoot) {
		t.Fatalf("profile=%+v want root=%s", p, want)
	}
	actual, err := loop.FindSkills(workspace, p.SkillsRoot)
	if err != nil || actual != p.SkillsRoot {
		t.Fatalf("published core rejects declared enabled root: %q %v", actual, err)
	}
	if p.SkillsVersion != "v-fixture" {
		t.Fatalf("version=%q", p.SkillsVersion)
	}
}

func fixtureVersion(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "skills-lock.json"), []byte(`{"version":"v-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRejectsInvalidSourcesWithoutFallback(t *testing.T) {
	for _, kind := range []string{"empty", "missing", "lock-only", "skill-without-adapters", "adapters-without-skill", "skill-is-directory", "wrong-layout"} {
		t.Run(kind, func(t *testing.T) {
			home, workspace, bad := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			skillFixture(t, filepath.Join(home, ".agents", "skills"), "batuta")
			skillFixture(t, filepath.Join(workspace, ".agents", "skills"), "batuta")
			switch kind {
			case "empty":
				bad = ""
			case "missing":
				bad = filepath.Join(bad, "missing")
			case "lock-only":
				fixtureVersion(t, bad)
			case "skill-without-adapters":
				if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "adapters-without-skill":
				if err := os.Mkdir(filepath.Join(bad, "adapters"), 0700); err != nil {
					t.Fatal(err)
				}
			case "skill-is-directory":
				for _, name := range []string{"adapters", "SKILL.md"} {
					if err := os.Mkdir(filepath.Join(bad, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "wrong-layout":
				skillFixture(t, filepath.Join(bad, "unrelated"), "batuta")
			}
			t.Setenv("BATUTA_SKILLS", bad)
			p := Prepare(workspace, true)
			if p.Enabled || p.SkillsRoot != "" || p.SkillsVersion != "" {
				t.Fatalf("invalid source enabled or fell through: %+v", p)
			}
			for _, sk := range ListSkills() {
				if sk.Installed {
					t.Fatalf("invalid installation listed: %+v", sk)
				}
			}
		})
	}
}

func TestPrepareSupportedLayoutsMatchPublishedCore(t *testing.T) {
	for _, kind := range []string{"canonical", "collection", "plugin", "relative", "symlink", "workspace-agents", "workspace-claude", "shared", "codex-plugin", "claude-plugin"} {
		t.Run(kind, func(t *testing.T) {
			home, ws, base := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			unsetSkills(t)
			collection := filepath.Join(base, "skills")
			switch kind {
			case "workspace-agents":
				collection = filepath.Join(ws, ".agents", "skills")
			case "workspace-claude":
				collection = filepath.Join(ws, ".claude", "skills")
			case "shared":
				collection = filepath.Join(home, ".agents", "skills")
			case "codex-plugin", "claude-plugin":
				host := ".codex"
				if kind == "claude-plugin" {
					host = ".claude"
				}
				cache := filepath.Join(home, host, "plugins", "cache", "batuta", "batuta")
				skillFixture(t, filepath.Join(cache, "0.9.0", "skills"), "batuta")
				collection = filepath.Join(cache, "0.10.0", "skills")
			}
			canonical := skillFixture(t, collection, "batuta", "batuta-init", "acme-lint")
			fixtureVersion(t, collection)
			switch kind {
			case "canonical":
				t.Setenv("BATUTA_SKILLS", canonical)
			case "collection":
				t.Setenv("BATUTA_SKILLS", collection)
			case "plugin":
				t.Setenv("BATUTA_SKILLS", base)
			case "relative":
				t.Chdir(base)
				t.Setenv("BATUTA_SKILLS", "skills/batuta")
			case "symlink":
				link := filepath.Join(base, "installed")
				if err := os.Symlink(canonical, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BATUTA_SKILLS", link)
			}
			assertCoreProfile(t, ws, canonical)
			if kind == "workspace-agents" || kind == "workspace-claude" {
				t.Chdir(ws)
			}
			byID := map[string]Skill{}
			for _, sk := range ListSkills() {
				byID[sk.ID] = sk
			}
			for _, name := range []string{"batuta", "batuta-init", "acme-lint"} {
				if !byID[name].Installed {
					t.Errorf("catalog missing %s: %+v", name, byID[name])
				}
			}
		})
	}
}

func TestNativeSkillsRevalidatesRemovedSource(t *testing.T) {
	root := skillFixture(t, t.TempDir(), "batuta")
	t.Setenv("BATUTA_SKILLS", root)
	p := Prepare(t.TempDir(), true)
	if !p.Enabled {
		t.Fatal("fixture not enabled")
	}
	if err := os.Remove(filepath.Join(root, "adapters")); err != nil {
		t.Fatal(err)
	}
	if selected, err := p.NativeSkills(nil); err == nil {
		t.Fatalf("removed dependency still authorized: %v", selected)
	}
}
