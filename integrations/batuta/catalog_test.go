package batuta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	skillFixture(t, root, "batuta")
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

// Language affects displayed summaries only, including when it changes after startup.
func TestCatalogSummaryLanguagePreservesIdentity(t *testing.T) {
	root := t.TempDir()
	skillFixture(t, root, "batuta")
	for _, id := range []string{"batuta-review", "acme-lint"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# User-owned português title\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BATUTA_SKILLS", root)
	t.Setenv("BATUTA_LANG", "en")
	before, err := json.Marshal(BuiltInSquads())
	if err != nil {
		t.Fatal(err)
	}
	official, err := json.Marshal(OfficialSkills)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := (Profile{SkillsRoot: SkillsRoot()}).NativeSkills([]string{"acme-lint"})
	if err != nil {
		t.Fatal(err)
	}
	english := ListSkills()
	wantEN := map[string]string{
		"batuta":         "Conducts the task without writing code: classifies, routes and verifies.",
		"batuta-init":    "Workspace profile and onboarding.",
		"batuta-plan":    "Implementation plan with acceptance criteria.",
		"batuta-loop":    "Delivery execution cycle.",
		"batuta-route":   "Executor and model routing.",
		"batuta-review":  "Review of a diff, PR or delivery.",
		"batuta-status":  "Mission and journal status.",
		"batuta-pause":   "Pause with a handoff for later resumption.",
		"batuta-resume":  "Resume from the handoff.",
		"batuta-qa-plan": "Delivery QA plan.",
		"batuta-qa-run":  "Execute the QA plan.",
		"acme-lint":      "Skill on disk, outside the Batuta catalog.",
	}
	wantPT := map[string]string{
		"batuta":         "Conduz a tarefa sem escrever o código — classifica, roteia e verifica.",
		"batuta-init":    "Perfil e onboarding do workspace.",
		"batuta-plan":    "Plano de implementação com critérios.",
		"batuta-loop":    "Ciclo de execução da entrega.",
		"batuta-route":   "Roteamento de executor e modelo.",
		"batuta-review":  "Revisão de diff, PR ou entrega.",
		"batuta-status":  "Estado da missão e do journal.",
		"batuta-pause":   "Pausa com handoff para retomar depois.",
		"batuta-resume":  "Retoma a partir do handoff.",
		"batuta-qa-plan": "Plano de QA da entrega.",
		"batuta-qa-run":  "Execução do plano de QA.",
		"acme-lint":      "Skill no disco, fora do catálogo Batuta.",
	}
	if len(english) != len(wantEN) {
		t.Fatalf("catalog size = %d", len(english))
	}
	for _, sk := range english {
		if sk.Summary != wantEN[sk.ID] {
			t.Errorf("English %s: %q", sk.ID, sk.Summary)
		}
	}
	t.Setenv("BATUTA_LANG", "pt-BR")
	portuguese := ListSkills()
	for i, sk := range portuguese {
		if sk.Summary != wantPT[sk.ID] {
			t.Errorf("Portuguese %s: %q", sk.ID, sk.Summary)
		}
		sk.Summary = english[i].Summary
		if sk != english[i] {
			t.Errorf("language changed identity or installation: %+v vs %+v", sk, english[i])
		}
	}
	after, err := json.Marshal(BuiltInSquads())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("language changed stored squad templates")
	}
	actualOfficial, err := json.Marshal(OfficialSkills)
	if err != nil {
		t.Fatal(err)
	}
	if string(official) != string(actualOfficial) {
		t.Fatal("listing mutated official catalog")
	}
	selectedAfter, err := (Profile{SkillsRoot: SkillsRoot()}).NativeSkills([]string{"acme-lint"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, selectedAfter) {
		t.Fatal("language changed native skill selection")
	}
	t.Setenv("BATUTA_LANG", "en")
	if !reflect.DeepEqual(english, ListSkills()) {
		t.Fatal("language switch retained stale summaries")
	}
}
