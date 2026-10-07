package i18n

func init() {
	for key, pair := range map[string][2]string{
		"catalog_local":             {"Local draft; no saved identity", "Draft local; sem identidade salva"},
		"catalog_external_recovery": {"Esc leaves text focus; then c/w opens account/workspace recovery", "Esc sai do texto; depois c/w abre recuperação de conta/workspace"},
		"catalog_editor":            {"Squad editor", "Editor de squad"},
		"catalog_name":              {"Name", "Nome"}, "catalog_purpose": {"Purpose", "Propósito"}, "catalog_role": {"Role", "Papel"}, "catalog_cli": {"CLI (choose Codex/Claude)", "CLI (escolha Codex/Claude)"}, "catalog_account": {"Account ID (matching CLI)", "ID da conta (mesma CLI)"}, "catalog_model": {"Model (explicit text)", "Modelo (texto explícito)"}, "catalog_effort": {"Effort (explicit text)", "Esforço (texto explícito)"}, "catalog_skills": {"Skills (comma separated)", "Skills (separadas por vírgula)"}, "catalog_maestro": {"Maestro", "Maestro"},
		"catalog_inherit":             {"empty / inherited; no model catalog", "vazio / herdado; sem catálogo de modelos"},
		"catalog_structural_draft":    {"Structural draft (saved catalog fact)", "Draft estrutural (fato do catálogo salvo)"},
		"catalog_readiness":           {"Fresh validation ready", "Validação recente pronta"},
		"catalog_validation_unknown":  {"Readiness not validated for these settings", "Prontidão não validada para estas configurações"},
		"catalog_name_required":       {"Enter a squad name before saving", "Informe um nome antes de salvar a squad"},
		"catalog_objective_required":  {"Enter an objective title and explicitly choose a catalog workspace and squad", "Informe o título do objetivo e escolha workspace e squad do catálogo"},
		"catalog_pending":             {"Catalog request pending; no duplicate dispatch", "Requisição de catálogo pendente; sem duplicação"},
		"catalog_read_failed":         {"Catalog read failed; last confirmed data retained", "Falha ao ler catálogo; últimos dados confirmados mantidos"},
		"catalog_validation_failed":   {"Validation failed; draft retained, nothing saved", "Validação falhou; draft mantido, nada salvo"},
		"catalog_uncertain":           {"Catalog effect unknown; do not repeat this operation", "Efeito no catálogo desconhecido; não repita esta operação"},
		"catalog_retry":               {"No effect confirmed; retry requires an explicit action", "Sem efeito confirmado; nova tentativa exige ação explícita"},
		"catalog_quarantined":         {"Unknown operation retained for inspection; acknowledgment does not confirm success or allow replay", "Operação desconhecida mantida para inspeção; ciência não confirma sucesso nem permite repetição"},
		"catalog_completed_elsewhere": {"Catalog result retained; select its actual ID explicitly", "Resultado do catálogo mantido; selecione seu ID real explicitamente"},
		"catalog_saved":               {"Saved catalog identity; structural Draft is separate from authenticated readiness", "Identidade salva no catálogo; Draft estrutural é separado da prontidão autenticada"},
		"catalog_created":             {"Mission catalog created with status open", "Missão criada no catálogo com status open"},
		"catalog_snapshot_changed":    {"Server snapshot differs from the reviewed squad; actual returned snapshot shown", "Snapshot do servidor difere da squad revisada; exibindo o snapshot real retornado"},
		"catalog_recovery_keys":       {"ctrl+f inspect catalog; ctrl+d acknowledge after inspection (no retry)", "ctrl+f inspecionar catálogo; ctrl+d registrar ciência após inspeção (sem repetir)"},
		"catalog_unverified_reply":    {"Unverified reply IDs", "IDs não confirmados da resposta"}, "catalog_confirmed": {"Confirmed catalog result", "Resultado confirmado do catálogo"},
		"catalog_new_mission": {"New mission", "Nova missão"}, "catalog_objective": {"Objective title", "Título do objetivo"}, "catalog_brief": {"Brief (optional, multiline)", "Descrição (opcional, multilinha)"}, "catalog_workspace": {"Workspace", "Workspace"}, "catalog_squad": {"Squad", "Squad"}, "catalog_snapshot": {"Actual snapshot", "Snapshot real"},
		"catalog_retained_editor": {"Retained squad editor (separate from mission selection)", "Editor de squad mantido (separado da seleção da missão)"},
		"catalog_confirm_mission": {"Confirm catalog creation: enter or ctrl+enter; esc to edit", "Confirmar criação no catálogo: enter ou ctrl+enter; esc para editar"},
		"catalog_snapshot_review": {"Server uses the current squad at creation; no binding, approval or execution", "Servidor usa a squad atual na criação; sem vínculo, aprovação ou execução"},
		"catalog_editor_keys":     {"tab field  left/right choose  space maestro  ctrl+up/down member  ctrl+n add  ctrl+x remove  ctrl+v validate  ctrl+s save  esc back", "tab campo  esquerda/direita escolher  espaço maestro  ctrl+cima/baixo membro  ctrl+n adicionar  ctrl+x remover  ctrl+v validar  ctrl+s salvar  esc voltar"},
		"catalog_mission_keys":    {"tab field  left/right choose  enter brief newline  enter on workspace/squad to review  enter to confirm review  ctrl+enter review/confirm  esc back", "tab campo  esquerda/direita escolher  enter nova linha na descrição  enter em workspace/squad para revisar  enter para confirmar revisão  ctrl+enter revisar/confirmar  esc voltar"},
		"catalog_list_keys":       {"n new  enter select actual ID  ctrl+f refresh/inspect  ctrl+d acknowledge", "n novo  enter selecionar ID real  ctrl+f atualizar/inspecionar  ctrl+d registrar ciência"},
	} {
		en[key] = pair[0]
		pt[key] = pair[1]
	}
}
