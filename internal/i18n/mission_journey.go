package i18n

func init() {
	for key, pair := range map[string][2]string{
		"journey_unconfirmed":         {"Unconfirmed response; inspect explicitly", "Resposta não confirmada; inspecione explicitamente"},
		"journey_question_changed":    {"Question changed; previous identity and draft retained. Inspect again", "Pergunta alterada; identidade e texto anteriores mantidos. Inspecione novamente"},
		"journey_not_done":            {"Completion is not verified", "Conclusão não verificada"},
		"journey_done":                {"Done — current cleared gate and evidence", "Concluído — gate atual liberado e evidências"},
		"journey_read_keys":           {"up/down pages  esc back", "cima/baixo páginas  esc voltar"},
		"journey_session_id":          {"Exact session ID: enter inspect, esc back", "ID exato da sessão: enter inspecionar, esc voltar"},
		"journey_native_snapshot":     {"Original approved snapshot", "Snapshot original aprovado"},
		"journey_current_unavailable": {"Current editable file bytes are unavailable through this API after native binding", "Bytes do arquivo editável atual indisponíveis nesta API após vínculo nativo"},
		"journey_new_answer":          {"Discard the previous draft and use the displayed current question", "Descartar texto anterior e usar a pergunta atual exibida"},
		"journey_keys":                {"p interactive planning  b bind existing plan  o read source  k receipt  h historical receipt  f inspect/refresh  e start  g resume  j answer  n new answer draft  w full progress/evidence  l inspect session", "p planejamento interativo  b vincular plano existente  o ler fonte  k recibo  h recibo histórico  f inspecionar/atualizar  e executar  g retomar  j responder  n nova resposta  w progresso/evidências completos  l inspecionar sessão"},
		"journey_pending":             {"Mission request pending; no duplicate dispatch", "Requisição da missão pendente; sem duplicação"},
		"journey_stale":               {"Read-only facts; refresh required for execution/answer authority", "Fatos somente leitura; atualize para autorizar execução/resposta"},
		"journey_failed":              {"Request failed; confirmed facts and draft retained", "Falha na requisição; fatos confirmados e texto mantidos"},
		"journey_uncertain":           {"Effect unknown; replay quarantined. Inspect exact session/receipt/delivery", "Efeito desconhecido; repetição bloqueada. Inspecione sessão/recibo/entrega exatos"},
		"journey_identity":            {"Select exactly one snapshot Maestro with an actual member/account/provider", "Selecione exatamente um Maestro do snapshot com membro/conta/provedor reais"},
		"journey_effect":              {"Last verified effect", "Último efeito verificado"},
		"journey_confirm":             {"Confirm", "Confirmar"}, "journey_confirm_keys": {"enter confirm  esc back", "enter confirmar  esc voltar"},
		"journey_planning": {"Start interactive planning session (no automatic command)", "Iniciar sessão de planejamento interativo (sem comando automático)"},
		"journey_bind":     {"Bind an existing exact plan", "Vincular plano exato existente"}, "journey_approve": {"Approve all inspected original source bytes", "Aprovar todos os bytes originais inspecionados"},
		"journey_start": {"Start native execution from this receipt", "Iniciar execução nativa deste recibo"}, "journey_resume": {"Resume the original delivery and opening", "Retomar entrega e abertura originais"}, "journey_answer": {"Record this answer; resume remains separate", "Registrar resposta; retomada continua separada"},
		"journey_source": {"Current source", "Fonte atual"}, "journey_receipt": {"Immutable approved snapshot", "Snapshot aprovado imutável"}, "journey_metadata": {"Full metadata/tasks (ASCII escaped JSON bytes)", "Metadados/tarefas completos (bytes JSON escapados em ASCII)"}, "journey_bytes": {"Original source bytes; offsets and lossless ASCII escapes", "Bytes da fonte original; posições e escapes ASCII sem perdas"},
		"journey_end": {"END OF SOURCE", "FIM DA FONTE"}, "journey_source_keys": {"up/down pages  y review approval after all pages  o reload current source  esc back", "cima/baixo páginas  y revisar aprovação após todas as páginas  o reler fonte atual  esc voltar"},
		"journey_path": {"Exact existing plan path", "Caminho exato do plano existente"}, "journey_delivery": {"Existing delivery (optional)", "Entrega existente (opcional)"}, "journey_review": {"Review / enter", "Revisar / enter"}, "journey_form_keys": {"tab field  enter on Review  esc back", "tab campo  enter em Revisar  esc voltar"},
		"journey_receipt_id": {"Exact receipt ID: enter read, esc back", "ID exato do recibo: enter ler, esc voltar"}, "journey_answer_keys": {"enter newline  tab review button  enter review/confirm  esc back", "enter nova linha  tab botão revisar  enter revisar/confirmar  esc voltar"},
		"journey_session": {"Confirmed planning session; [ / ] select, v attach explicitly", "Sessão de planejamento confirmada; [ / ] selecionar, v anexar explicitamente"}, "journey_execution": {"Native delivery / original opening", "Entrega nativa / abertura original"}, "journey_source_identity": {"Watch mission/workspace/delivery/source", "Missão/workspace/entrega/fonte da projeção"},
		"journey_unrelated": {"Separate terminal session belongs to another mission", "Sessão de terminal separada pertence a outra missão"},
	} {
		en[key] = pair[0]
		pt[key] = pair[1]
	}
}
