# WORK — Batuta Console

## In progress
- [ ] Executar o plano aprovado de 41 tarefas com TDD e versões atuais → condutor self; proposta em .batuta/plans/estabilizar-console.md.
- [ ] Iniciar a estabilização funcional pelo core publicado e toolchain atual → sequential, roteamento em .batuta/routing.md; tarefa 1 integrada em ddf03a9; tarefa 2 qualificada com Go 1.27.1, sem vulnerabilidades no scan de fonte; tarefa 3 é a próxima.
- [ ] Revisitar a distribuição visual do web e da TUI depois do fechamento funcional → fase 2 de .batuta/roadmap.md, ainda sem desenho aprovado.
- [ ] Qualificar o candidato final e definir remoto/tags/compatibilidade core/plugin para publicação → fase 3 do roadmap; publicação pendente.

## Done
- [x] Tarefa 2 — Go 1.27.1, testes/vet verdes e scanner sem vulnerabilidades após red com seis achados → codex/gpt-6.1-sol low, uma revisão de documentação, claude/sonnet; commit ab17681 (trilha: .batuta/runs/2026-10-03-go-toolchain.md).
- [x] Tarefa 1 — atualizar core para v1.1.0-beta.46 e remover replace local; build/test/install isolados com red/green e proteção contra replacement/vendor/cache alterado → codex/gpt-6-astra high, escalado de codex/gpt-6.1-sol por limitações de sandbox; revisão claude/sonnet, commit ddf03a9 (trilha: .batuta/runs/2026-10-03-published-core.md).
- [x] Registrar configuração/planejamento/auditoria aprovados → condutor self, commit d0af2af (chore(batuta): set up console stabilization).
- [x] Aprovar plano/commit inicial e definir TDD obrigatório e bibliotecas atuais → decisão do usuário em 2026-10-03; sem nova confirmação necessária.
- [x] Definir matriz da primeira versão estável em 2026-10-03 → decisão do usuário: Codex e Claude, macOS e Linux, operação completa no web e na TUI.
- [x] Auditar o projeto para publicação estável: 28 achados, 6 P0, 14 P1 e 8 P2 → condutor self e revisão independente claude/sonnet; sem commit (relatórios: docs/release-readiness.md e docs/release-readiness-evidence.md; trilha: .batuta/runs/2026-10-02-release-readiness.review.md).
- [x] Concluir Batuta init e preparar plano proposto em 2026-10-03 → condutor self; inventário de todos os adapters, mapa verificado após dois timeouts opencode/ling-3.1-flash-free e um timeout codex/gpt-6.1-sol, guards limpos; self-check, stamps, modelos e parsers do core validados; sem commit e sem ponteiro AGENTS.md, ambos opcionais sem autorização específica.

## Contexto para retomar
O usuário pediu iniciar Batuta init, planejar a correção dos bloqueadores e depois revisar o painel web e a TUI. O usuário aprovou o plano e commit inicial em 2026-10-03, exigindo TDD sempre e releases mais recentes das bibliotecas. O plano está in progress e os aceites descrevem provas futuras. O usuário escolheu Codex e Claude, macOS e Linux, e operação completa no web e na TUI. A revisão incluiu contratos do cliente, emulação de terminal e onboarding/contas/workspace/missão/aprovação/execução/perguntas na TUI, totalizando 41 tarefas após incluir a atualização explícita das bibliotecas. A versão estável exata de core/plugin e o destino de publicação continuam pendentes. As falhas de race e govulncheck da auditoria não foram corrigidas nesta etapa de planejamento. Não usar login/consumo de modelo real sem conta de teste escolhida na ocasião.

Estado atual: TDD obrigatório, versões atuais consultadas em fontes oficiais e fixadas; core beta.46 integrado. Go 1.27.1 foi qualificado na tarefa 2 e govulncheck v1.8.0 está em /tmp/batuta-console-release-tools. O red .batuta/runs/2026-10-03-go-toolchain.red.log reporta as seis vulnerabilidades com Go 1.26.4. Lyra React/styles 1.1.0, Vite 8.3.2, TypeScript 7.0.2 e demais bibliotecas entram na tarefa 4 após a base de testes web da tarefa 3. Os manifestos web ainda não foram atualizados. Não houve publicação.
