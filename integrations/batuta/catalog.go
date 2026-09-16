package batuta

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
)

// Skill is a named capability the operator assigns to a squad member.
// Official Batuta skills are always listed; extra directories on disk
// appear as custom. Custom names typed in the editor need not exist yet.
type Skill struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Origin    string `json:"origin"`
	Summary   string `json:"summary"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
}

// OfficialSkills is the Batuta skill catalog shown in the Console.
var OfficialSkills = []Skill{
	{ID: "batuta", Name: "batuta", Origin: "batuta", Summary: "Conduz a tarefa sem escrever o código — classifica, roteia e verifica."},
	{ID: "batuta-init", Name: "batuta-init", Origin: "batuta", Summary: "Perfil e onboarding do workspace."},
	{ID: "batuta-plan", Name: "batuta-plan", Origin: "batuta", Summary: "Plano de implementação com critérios."},
	{ID: "batuta-loop", Name: "batuta-loop", Origin: "batuta", Summary: "Ciclo de execução da entrega."},
	{ID: "batuta-route", Name: "batuta-route", Origin: "batuta", Summary: "Roteamento de executor e modelo."},
	{ID: "batuta-review", Name: "batuta-review", Origin: "batuta", Summary: "Revisão de diff, PR ou entrega."},
	{ID: "batuta-status", Name: "batuta-status", Origin: "batuta", Summary: "Estado da missão e do journal."},
	{ID: "batuta-pause", Name: "batuta-pause", Origin: "batuta", Summary: "Pausa com handoff para retomar depois."},
	{ID: "batuta-resume", Name: "batuta-resume", Origin: "batuta", Summary: "Retoma a partir do handoff."},
	{ID: "batuta-qa-plan", Name: "batuta-qa-plan", Origin: "batuta", Summary: "Plano de QA da entrega."},
	{ID: "batuta-qa-run", Name: "batuta-qa-run", Origin: "batuta", Summary: "Execução do plano de QA."},
}

// SkillsRoot is the directory used for session injection and skill discovery.
func SkillsRoot() string { return skillsRoot() }

// ListSkills returns official Batuta skills plus extra SKILL.md trees on disk.
func ListSkills() []Skill {
	installed := discoverSkills(skillsRoot())
	seen := map[string]bool{}
	out := make([]Skill, 0, len(OfficialSkills)+len(installed))
	for _, sk := range OfficialSkills {
		if p, ok := installed[sk.ID]; ok {
			sk.Installed = true
			sk.Path = p
		}
		seen[sk.ID] = true
		out = append(out, sk)
	}
	ids := make([]string, 0, len(installed))
	for id := range installed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if seen[id] {
			continue
		}
		out = append(out, Skill{
			ID: id, Name: id, Origin: "custom", Installed: true, Path: installed[id],
			Summary: "Skill no disco, fora do catálogo Batuta.",
		})
	}
	return out
}

func discoverSkills(root string) map[string]string {
	found := map[string]string{}
	if root == "" {
		return found
	}
	scanSkillDir(root, found)
	scanSkillDir(filepath.Join(root, "skills"), found)
	return found
}

func scanSkillDir(dir string, found map[string]string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err != nil {
			continue
		}
		if _, ok := found[e.Name()]; !ok {
			found[e.Name()] = p
		}
	}
}

func member(id, role, cli string, maestro bool, skills ...string) domain.Member {
	return domain.Member{
		ID: id, Role: role, CLI: cli, Maestro: maestro, Effort: "medium", Skills: skills,
	}
}

// BuiltInSquads are the Batuta templates seeded into an empty-id catalog slot.
// Account IDs stay empty until a unique connected account for that CLI exists.
func BuiltInSquads() []domain.Squad {
	return []domain.Squad{
		{
			ID: "sq_batuta_ciclo", Name: "Ciclo Batuta", Origin: domain.OriginBatuta, Version: 1,
			Purpose: "Conduzir, executar e revisar uma entrega com as skills oficiais do Batuta.",
			Members: []domain.Member{
				member("mb_ciclo_maestro", "Maestro", "codex", true, "batuta", "batuta-init", "batuta-plan", "batuta-loop", "batuta-status", "batuta-pause", "batuta-resume"),
				member("mb_ciclo_revisor", "Revisor", "claude", false, "batuta-review"),
			},
		},
		{
			ID: "sq_batuta_entrega_web", Name: "Entrega web", Origin: domain.OriginBatuta, Version: 1,
			Purpose: "Ship de interface: maestro, builder, frontend e revisor.",
			Members: []domain.Member{
				member("mb_web_maestro", "Maestro", "codex", true, "batuta", "batuta-plan", "batuta-loop"),
				member("mb_web_builder", "Builder", "codex", false, "batuta-loop"),
				member("mb_web_frontend", "Frontend", "codex", false, "batuta-loop"),
				member("mb_web_revisor", "Revisor", "claude", false, "batuta-review"),
			},
		},
		{
			ID: "sq_batuta_backend", Name: "Backend", Origin: domain.OriginBatuta, Version: 1,
			Purpose: "Serviço e dados: maestro, builder e tester com plano e run de QA.",
			Members: []domain.Member{
				member("mb_be_maestro", "Maestro", "codex", true, "batuta", "batuta-plan", "batuta-loop"),
				member("mb_be_builder", "Builder", "codex", false, "batuta-loop"),
				member("mb_be_tester", "Tester", "codex", false, "batuta-qa-plan", "batuta-qa-run"),
			},
		},
		{
			ID: "sq_batuta_revisao", Name: "Revisão rápida", Origin: domain.OriginBatuta, Version: 1,
			Purpose: "Revisor e tester sobre um diff ou PR, sem abrir um ciclo completo.",
			Members: []domain.Member{
				member("mb_rev_revisor", "Revisor", "claude", true, "batuta-review"),
				member("mb_rev_tester", "Tester", "codex", false, "batuta-qa-plan", "batuta-qa-run"),
			},
		},
	}
}
