package i18n

func init() {
	for key, pair := range map[string][2]string{
		"skill_summary_batuta":         {"Conducts the task without writing code: classifies, routes and verifies.", "Conduz a tarefa sem escrever o código — classifica, roteia e verifica."},
		"skill_summary_batuta-init":    {"Workspace profile and onboarding.", "Perfil e onboarding do workspace."},
		"skill_summary_batuta-plan":    {"Implementation plan with acceptance criteria.", "Plano de implementação com critérios."},
		"skill_summary_batuta-loop":    {"Delivery execution cycle.", "Ciclo de execução da entrega."},
		"skill_summary_batuta-route":   {"Executor and model routing.", "Roteamento de executor e modelo."},
		"skill_summary_batuta-review":  {"Review of a diff, PR or delivery.", "Revisão de diff, PR ou entrega."},
		"skill_summary_batuta-status":  {"Mission and journal status.", "Estado da missão e do journal."},
		"skill_summary_batuta-pause":   {"Pause with a handoff for later resumption.", "Pausa com handoff para retomar depois."},
		"skill_summary_batuta-resume":  {"Resume from the handoff.", "Retoma a partir do handoff."},
		"skill_summary_batuta-qa-plan": {"Delivery QA plan.", "Plano de QA da entrega."},
		"skill_summary_batuta-qa-run":  {"Execute the QA plan.", "Execução do plano de QA."},
		"skill_summary_custom":         {"Skill on disk, outside the Batuta catalog.", "Skill no disco, fora do catálogo Batuta."},
	} {
		en[key] = pair[0]
		pt[key] = pair[1]
	}
}
