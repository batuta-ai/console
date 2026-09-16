package batuta

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltInSquadsHaveMaestroAndSkills(t *testing.T) {
	seen := map[string]bool{}
	for _, q := range BuiltInSquads() {
		if q.ID == "" || q.Origin != "batuta" || q.Name == "" {
			t.Fatalf("incomplete squad %+v", q)
		}
		if seen[q.ID] {
			t.Fatalf("duplicate id %s", q.ID)
		}
		seen[q.ID] = true
		maestro := 0
		for _, m := range q.Members {
			if m.ID == "" || m.Role == "" || m.CLI == "" {
				t.Fatalf("%s member incomplete %+v", q.ID, m)
			}
			if len(m.Skills) == 0 {
				t.Fatalf("%s %s has no skills", q.ID, m.Role)
			}
			if m.Maestro {
				maestro++
			}
		}
		if maestro != 1 {
			t.Fatalf("%s maestro count %d", q.ID, maestro)
		}
	}
}

func TestListSkillsMarksInstalledAndCustom(t *testing.T) {
	root := t.TempDir()
	mustSkill := func(name string) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustSkill("batuta")
	mustSkill("batuta-review")
	mustSkill("acme-lint")
	t.Setenv("BATUTA_SKILLS", root)

	list := ListSkills()
	byID := map[string]Skill{}
	for _, sk := range list {
		byID[sk.ID] = sk
	}
	if !byID["batuta"].Installed || byID["batuta"].Origin != "batuta" {
		t.Fatalf("batuta %+v", byID["batuta"])
	}
	if byID["batuta-plan"].Installed {
		t.Fatalf("missing official skill should stay listed but not installed: %+v", byID["batuta-plan"])
	}
	custom := byID["acme-lint"]
	if !custom.Installed || custom.Origin != "custom" {
		t.Fatalf("custom %+v", custom)
	}
}

func TestOfficialSkillIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, sk := range OfficialSkills {
		if seen[sk.ID] {
			t.Fatalf("duplicate %s", sk.ID)
		}
		seen[sk.ID] = true
	}
}
