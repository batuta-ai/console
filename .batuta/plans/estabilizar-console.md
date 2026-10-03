# Plan — Corrigir bloqueadores e completar o fluxo funcional
<!-- inputs: profile.md@sha256:26e0c396482c routing.md@sha256:517064b8c524 -->

**Goal:** Tornar o Batuta Console funcional e verificável para a publicação coordenada com o Batuta estável. Corrigir os problemas de execução, isolamento, persistência, inicialização e identidade das missões; completar o ciclo de plano, aprovação, execução, perguntas e recuperação; preparar a distribuição reproduzível antes da revisão visual do web e da TUI.
**Created:** 2026-10-03 · **Status:** in progress

## Tasks
- [x] 1. Fixar o Batuta publicado mais recente e remover checkout irmão — infra/medium
      Scope: go.mod, go.sum, scripts/check-standalone.sh, README.md
      Accept: Testes e os dois binários funcionam em cópia isolada com GOWORK=off e sem core local → sh scripts/check-standalone.sh; A dependência usa a release publicada mais recente verificada, e o script falha antes da correção por depender do core local, sem reutilizar binários previamente construídos
- [ ] 2. Atualizar e qualificar a toolchain Go estável mais recente — infra/medium
      Depends on: 1
      Scope: go.mod, go.sum, README.md, docs/release-readiness.md, docs/release-readiness-evidence.md
      Accept: A toolchain estável mais recente verificada corrige os seis achados R02 e qualquer novo achado é avaliado sem supressão genérica → govulncheck ./...; A suíte existente permanece verde → go test ./...
- [ ] 3. Criar infraestrutura mínima de testes web e de jornada — testing/medium
      Scope: web/package.json, web/package-lock.json, web/*config*, web/src/test/**, web/tests/**, web/src/**/*.test.ts, web/src/**/*.test.tsx
      Accept: O runner cobre uma interação significativa de produto e uma resposta API com erro, em vez de apenas renderizar um componente → npm --prefix web run test:run; A jornada usa daemon e contas temporários, evita chamadas pagas por padrão e separa a qualificação real do CLI
- [ ] 4. Atualizar e qualificar as bibliotecas publicadas mais recentes — fullstack/high
      Depends on: 1, 2, 3
      Scope: go.mod, go.sum, web/package.json, web/package-lock.json, web/src/**, web/*config*, web/tests/**, internal/**, cmd/**, integrations/**, plugins/**, docs/dependency-policy.md
      Accept: Dependências diretas, incluindo Lyra React/styles e Batuta, usam as releases mais recentes verificadas em fontes oficiais, com versões e breaking changes registrados; Ajustes de compatibilidade seguem teste primeiro e não escondem falhas → go test ./...; Suite web prova os contratos usados antes/depois da migração → npm --prefix web run test:run; Typecheck passa → npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json; Build web passa → npm --prefix web run build; Grafo/locks são reproduzíveis e nenhum downgrade é aplicado sem decisão explícita
- [ ] 5. Introduzir migrações versionadas e falhas observáveis — data/high
      Depends on: 1, 4
      Scope: internal/store/**
      Accept: Fixtures de esquemas anteriores preservam contas, workspaces, missões e sessões, e uma migração inválida falha claramente → go test ./internal/store -count=1; Escritas importantes e seus eventos usam uma política consistente de transação, com falhas propagadas e retenção documentada
- [ ] 6. Corrigir contenção e consultas SQLite — data/high
      Depends on: 5
      Scope: internal/store/**, internal/api/server_test.go
      Accept: Listagem concorrente com término natural não produz SQLITE_BUSY nas repetições do caso R10 → go test -race ./internal/api -run TestNaturalExitIsExited -count=20; Consultas fecham rows antes de consultas aninhadas e escritas seguem a política definida → go test -race ./internal/store -count=5
- [ ] 7. Tornar reserva de capacidade e estado inicial da sessão atômicos — backend/high
      Depends on: 6
      Scope: internal/session/**, internal/service/**, internal/store/**
      Accept: Com limite 3 e 10 starts concorrentes, no máximo 3 processos iniciam, e reserva falhada é liberada → go test -race ./internal/service -count=10; Saída imediata nunca volta a running e o teste OnExit usa sincronização real → go test -race ./internal/session -count=10
- [ ] 8. Reconciliar sessões e capacidade antes de aceitar requisições — backend/high
      Depends on: 7
      Scope: internal/service/**, internal/store/**, internal/api/**, cmd/consoled/**
      Accept: Reinício com catálogo contendo sessões running recupera capacidade antes de qualquer GET de sessões → go test -race ./internal/service ./internal/api -count=5; Sessões perdidas ganham estado explícito sem encerrar um processo alheio por reutilização de PID
- [ ] 9. Encerrar a árvore de processos da sessão — backend/high
      Depends on: 7
      Scope: internal/session/**, cmd/consoled/**
      Accept: Stop encerra líder e filho que ignora HUP, repetições de stop são seguras e não atingem outra sessão → go test -race ./internal/session -count=10; Desconexão de um cliente preserva a sessão e redimensionamento continua funcionando
- [ ] 10. Aplicar isolamento de credenciais por conta — security/critical
      Depends on: 7
      Scope: internal/accounts/**, internal/adapters/**, internal/service/**
      Accept: Processos isolados não herdam credenciais de provedores do daemon e contas A/B não compartilham identidade → go test -race ./internal/adapters ./internal/service -count=5; A allowlist preserva o ambiente necessário ao CLI sem copiar segredos da conta pessoal
- [ ] 11. Serializar criação e validar permissões do token local — security/critical
      Scope: internal/runtime/**, cmd/consoled/**, cmd/console/ensure.go, cmd/console/ensure_test.go
      Accept: Arquivo existente permissivo é corrigido ou recusado e startups simultâneos observam um único token → go test -race ./internal/runtime ./cmd/console -count=10; Testes usam diretórios e segredos descartáveis e não alteram credenciais reais
- [ ] 12. Validar referências antes de iniciar processos — backend/medium
      Depends on: 7, 10
      Scope: internal/domain/**, internal/service/**, internal/api/**
      Accept: Workspace, missão, membro e conta inexistentes ou incompatíveis falham antes de spawn, e membro default fica persistido → go test ./internal/domain ./internal/service ./internal/api -count=1; Sessão genérica tem regras explícitas e não simula uma missão válida
- [ ] 13. Construir argv por adapter e eliminar fallback silencioso — backend/high
      Depends on: 10, 12
      Scope: internal/adapters/**, internal/service/**, internal/domain/**
      Accept: CLI falso recebe objetivo, modelo e effort efetivos, e executável ausente retorna erro em vez de iniciar shell → go test ./internal/adapters ./internal/service -count=1; Flags de cada adapter suportado são conferidas na ajuda/documentação oficial da versão instalada, com configuração efetiva reportada
- [ ] 14. Definir readiness e autenticação dos adapters suportados — security/critical
      Depends on: 10, 13
      Scope: internal/adapters/**, internal/accounts/**, internal/service/**, internal/domain/**, internal/api/**
      Accept: Disponível, conectado, autenticado e não suportado são estados distintos, sem tratar arquivo não vazio como prova de login → go test ./internal/adapters ./internal/service ./internal/api -count=1; Credencial inválida, expirada, CLI ausente e adapter fora da matriz impedem execução com diagnóstico recuperável
- [ ] 15. Corrigir descoberta e carregamento dos skills em HOME isolado — backend/high
      Depends on: 1, 10, 13
      Scope: integrations/batuta/inject.go, integrations/batuta/*_test.go, internal/service/**
      Accept: O caminho declarado como enabled é aceito pelo FindSkills da dependência publicada e fixtures cobrem ausência e estrutura incorreta → go test ./integrations/batuta ./internal/service -count=1; Conta isolada encontra os skills autorizados sem depender do HOME pessoal ou apenas da variável BATUTA_SKILLS
- [ ] 16. Modelar inicialização como operação recuperável — backend/high
      Depends on: 12, 14, 15
      Scope: internal/service/**, internal/domain/**, internal/api/**, integrations/batuta/**
      Accept: Diretório inexistente ou arquivo é recusado e estado ready exige profile, routing e WORK válidos → go test ./internal/service ./internal/api ./integrations/batuta -count=1; Cancelar, falhar ou ter profile parcial permite retomar com conta explicitamente selecionada, sem fallback de identidade do host
- [ ] 17. Completar criação e retomada de workspace no web — frontend/high
      Depends on: 4, 16, 3
      Scope: web/src/**, web/tests/**, internal/webui/dist/**
      Accept: Workspace novo e existente incompleto alcançam setup, mostram todos os campos necessários, confirmam configuração e permitem falha/cancelamento/retomada → npm --prefix web run test:run; Estado de conta/readiness e erros do init refletem o contrato backend → npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json
- [ ] 18. Vincular missão ao plano e delivery exatos — data/high
      Depends on: 5, 12, 16
      Scope: internal/domain/**, internal/store/**, internal/service/**, integrations/batuta/**, internal/api/**
      Accept: Duas missões do mesmo workspace e missões de workspaces diferentes exibem apenas seu próprio plano, journal e evidência → go test ./internal/store ./internal/service ./integrations/batuta ./internal/api -count=1; Antes de existir delivery, a missão mostra esse estado e nunca adota o journal mais recente por proximidade
- [ ] 19. Exigir identidade estável e validação de perguntas — backend/high
      Depends on: 18
      Scope: internal/domain/**, integrations/batuta/**, internal/service/**, internal/api/**, internal/client/**
      Accept: Resposta exige missão, delivery e question_id corretos, recusa pergunta antiga ou de outra missão e cobre duplicação → go test ./integrations/batuta ./internal/service ./internal/api ./internal/client -count=1; A falta de identidade não dispara busca global de journal nem responde outra pergunta com task_id coincidente
- [ ] 20. Expor detalhe e aprovação de um plano específico — backend/high
      Depends on: 1, 16, 18
      Scope: integrations/batuta/**, internal/domain/**, internal/service/**, internal/api/**, internal/client/**
      Accept: Detalhe mostra tarefas/escopo/dependências/aceites e aprovação valida slug, conteúdo atual e transição de estado → go test ./integrations/batuta ./internal/service ./internal/api ./internal/client -count=1; Conteúdo alterado, path traversal ou plano de outro workspace não é aprovado, e a ação autenticada deixa evidência
- [ ] 21. Integrar execução e retomada nativas do core — backend/critical
      Depends on: 13, 15, 18, 19, 20
      Scope: integrations/batuta/**, internal/service/**, internal/domain/**, internal/api/**, internal/client/**
      Accept: Plano proposto não executa, plano aprovado inicia a entrega correta e uma entrega pausada retoma sem duplicar execução → go test ./integrations/batuta ./internal/service ./internal/api ./internal/client -count=1; O journal e gates do core permanecem autoridade, com comandos/capabilities validados no core selecionado e sem motor paralelo de tarefas no Console
- [ ] 22. Completar a jornada de missão no painel web — frontend/high
      Depends on: 3, 17, 19, 20, 21
      Scope: web/src/**, web/tests/**, internal/webui/dist/**
      Accept: Usuário cria objetivo, lê plano, aprova a versão exata, executa, responde pergunta identificada e retoma, com falhas visíveis → npm --prefix web run test:run; Estados proposto/aprovado/em execução/aguardando/concluído correspondem ao backend e respeitam ações permitidas → npm --prefix web exec -- tsc --noEmit -p web/tsconfig.json
- [ ] 23. Corrigir cursor e encerramento do socket de eventos — backend/medium
      Depends on: 6
      Scope: internal/api/**, internal/store/**, internal/client/**
      Accept: Cursor válido retoma sem replay integral e conexão idle fechada cancela o handler sem vazamento → go test -race ./internal/api ./internal/store ./internal/client -count=5; Snapshot inicial é limitado e falhas de leitura/escrita são observáveis
- [ ] 24. Atualizar progresso do core e proteger o web contra respostas antigas — frontend/high
      Depends on: 3, 18, 19, 22, 23
      Scope: web/src/**, web/tests/**, internal/webui/dist/**
      Accept: Escrita externa no journal atualiza missão/atenção enquanto o processo continua vivo e restart do daemon reconecta sem reload → npm --prefix web run test:run; Troca rápida de workspace ou missão não aplica resposta atrasada ao item atual, e polling/reconnect são limitados e canceláveis
- [ ] 25. Corrigir identidade, controle e estados assíncronos da UI — frontend/high
      Depends on: 3, 22, 24
      Scope: web/src/**, web/tests/**, internal/webui/dist/**
      Accept: Dois cadastros consecutivos limpam o diálogo, seleção de membro escolhe a sessão correta e tarefa usa identidade real → npm --prefix web run test:run; Transferência entre clientes reais respeita propriedade, não há botão aninhado e falhas/pending/retries impedem duplicação de ações
- [ ] 26. Publicar capabilities honestas para a matriz escolhida — backend/medium
      Depends on: 14
      Scope: internal/adapters/**, internal/session/pty_unix.go, internal/session/pty_windows.go, internal/domain/**, internal/api/**, README.md
      Accept: Capabilities distinguem plataforma compilável, PTY disponível e adapter qualificado, com Windows fora da matriz selecionada → go test ./internal/adapters ./internal/api -count=1; Interfaces não oferecem ações que o ambiente não pode executar
- [ ] 27. Completar contratos do cliente para a operação da TUI — backend/medium
      Depends on: 16, 19, 20, 21, 23, 26
      Scope: internal/client/**, internal/domain/**, internal/api/server_test.go
      Accept: Cliente cobre contas/login, workspace/init, squads/missões, detalhe/aprovação de plano, execução/retomada e pergunta exata com erros tipados → go test ./internal/client ./internal/domain -count=1; Requests têm cancelamento e testes com servidor temporário verificam corpos e identidades, sem depender de estado global
- [ ] 28. Definir o contrato técnico do terminal interativo da TUI — frontend/critical
      Depends on: 9, 13, 26, 27
      Scope: docs/tui-terminal.md
      Accept: Documento compara suporte disponível e registra a escolha de emulação compatível com Bubble Tea, input/foco/resize/scroll e alternate screen; Casos de prova incluem Unicode, cursor, clear screen, sinais, reconexão e interação com Codex e Claude, com riscos e versões verificadas nas fontes oficiais
- [ ] 29. Implementar emulação e controle de terminal na TUI — frontend/high
      Depends on: 9, 27, 28
      Scope: internal/tui/**, internal/client/term.go, internal/client/*_test.go, go.mod, go.sum, scripts/qualify-terminal.sh
      Accept: Fixtures de cursor/clear/alternate screen/Unicode/resize mantêm buffer de tela correto e input não conflita com atalhos do painel → go test ./internal/tui ./internal/client -count=1; Attach/detach, transferência de controle e stop preservam ownership e o processo correto → sh scripts/qualify-terminal.sh --fixtures
- [ ] 30. Implementar onboarding e contas Codex/Claude na TUI — frontend/high
      Depends on: 14, 27, 29
      Scope: internal/tui/**, internal/i18n/**, cmd/console/**
      Accept: Instalação vazia cria conta isolada, conecta e abre login oficial para Codex/Claude, com estados e recuperação de erro → go test ./internal/tui ./internal/i18n ./cmd/console -count=1; Dois cadastros consecutivos não reutilizam o draft e ações pending não duplicam contas ou sessões de login
- [ ] 31. Implementar criação, inicialização e retomada de workspace na TUI — frontend/high
      Depends on: 16, 27, 29, 30
      Scope: internal/tui/**, internal/i18n/**, internal/client/*_test.go
      Accept: Usuário cria/seleciona diretório, informa e confirma campos do init e retoma workspace parcial sem sair para o web → go test ./internal/tui ./internal/i18n ./internal/client -count=1; Cancelamento, conta incompatível e CLI ausente retornam estado recuperável com foco e formulário preservados corretamente
- [ ] 32. Completar squads e criação de missão com objetivo na TUI — frontend/high
      Depends on: 12, 18, 27, 30, 31
      Scope: internal/tui/**, internal/i18n/**, internal/client/*_test.go
      Accept: Editor configura membros/contas/model/effort necessários e cria missão com objetivo real no workspace escolhido → go test ./internal/tui ./internal/i18n ./internal/client -count=1; Validação, pending e erro preservam dados sem duplicar missão, e seleção entre missões mantém a identidade correta
- [ ] 33. Completar aprovação, execução, perguntas e retomada na TUI — frontend/high
      Depends on: 19, 20, 21, 23, 27, 29, 31, 32
      Scope: internal/tui/**, internal/i18n/**, internal/client/*_test.go
      Accept: TUI lê e aprova plano exato, executa, responde delivery/question_id corretos, retoma e mostra gates/evidências de duas missões sem mistura → go test ./internal/tui ./internal/i18n ./internal/client -count=1; Journal externo, restart do daemon e reconexão restauram progresso, preservam seleção e recusam resposta atrasada
- [ ] 34. Distinguir observações de quota atuais, antigas e desconhecidas — backend/medium
      Scope: plugins/limits/**, internal/domain/view.go, docs/domain.md
      Accept: Fixture antiga vira stale, tempo da medição não é substituído pelo tempo de leitura e valores/schema inválidos viram unknown → go test ./plugins/limits -count=1; Limite de sessões não vira quota do provedor e o modo importado/desconhecido está documentado
- [ ] 35. Atualizar documentação de produto e textos PT/EN — docs/medium
      Depends on: 17, 22, 25, 26, 33, 34
      Scope: README.md, docs/api.md, docs/domain.md, internal/i18n/**, integrations/batuta/catalog.go, integrations/batuta/catalog_test.go, web/src/i18n.ts, web/src/App.tsx, web/src/ui.tsx, internal/webui/dist/**
      Accept: Docs descrevem login/init/aprovação/execução/perguntas/recuperação, erros e matriz suportada sem fallback de embed impossível; Fluxos novos têm textos PT/EN e não codificam resumos sempre em português → go test ./internal/i18n ./integrations/batuta; Tipagem e build web passam → npm --prefix web run build
- [ ] 36. Acrescentar diagnóstico de versão e compatibilidade — infra/medium
      Depends on: 1, 26
      Scope: cmd/console/**, cmd/consoled/**, internal/buildinfo/**, internal/api/**, docs/release.md
      Accept: console e consoled reportam versão/commit/toolchain e API expõe o contrato compatível de Console/core/plugin → go test ./cmd/console ./cmd/consoled ./internal/api ./internal/buildinfo; Builds de desenvolvimento têm valores explícitos e release injeta os metadados sem inventar a versão estável do core
- [ ] 37. Automatizar checks e pacote com os dois binários — infra/high
      Depends on: 2, 1, 8, 9, 11, 3, 26, 35, 36
      Scope: .github/workflows/**, scripts/**, Makefile, .goreleaser.yaml, docs/release.md, README.md, internal/webui/dist/**
      Accept: Pipeline reproduz install/typecheck/web build/Go tests/vet/race/scan e verifica assets sem drift → sh scripts/check-release.sh; Pacote local inclui console, consoled, web embarcado, licenças e checksums para cada alvo suportado → sh scripts/package-release.sh
- [ ] 38. Automatizar a jornada completa com contas e CLIs descartáveis — testing/high
      Depends on: 8, 9, 11, 15, 17, 21, 22, 24, 25, 33, 37
      Scope: web/tests/**, internal/tui/*_test.go, scripts/qualify-release.sh, docs/release-qualification.md
      Accept: Jornada web cobre duas missões, pergunta, stop e restart com daemon/contas/CLIs temporários → npm --prefix web run test:e2e; Jornada TUI cobre os mesmos contratos desde instalação vazia e o bundle local encontra seu consoled → sh scripts/qualify-release.sh --fixtures
- [ ] 39. Qualificar Codex e Claude em macOS real — testing/critical
      Depends on: 38
      Scope: docs/release-qualification.md, docs/release-readiness-evidence.md, scripts/qualify-release.sh
      Accept: macOS executa login oficial isolado de Codex e Claude, init, modelo/skills e missão completa no web e na TUI, com versões e resultados registrados; Bundle instala em diretório limpo, inicia seu daemon e passa stop/restart/controle interativo com contas de teste explicitamente selecionadas
- [ ] 40. Qualificar Codex e Claude em Linux real — testing/critical
      Depends on: 38
      Scope: docs/release-qualification.md, docs/release-readiness-evidence.md, scripts/qualify-release.sh
      Accept: Linux executa login oficial isolado de Codex e Claude, init, modelo/skills e missão completa no web e na TUI, com versões e resultados registrados; Bundle instala em diretório limpo, inicia seu daemon e passa stop/restart/controle interativo, com browser e terminal reais ou ambiente equivalente documentado
- [ ] 41. Fechar os achados e autorizar a passagem para revisão visual — docs/medium
      Depends on: 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40
      Scope: docs/release-readiness.md, docs/release-readiness-evidence.md, docs/release-qualification.md, docs/release.md
      Accept: Cada R01–R28 tem disposição, evidência e limite explícito, sem P0 funcional aberto e sem P1 aberto de recurso anunciado, com provas de publicação externa separadas na fase 3; O fechamento distingue preparação funcional de publicação final e registra R27 para a fase visual; Checks finais passam sobre o candidato funcional → sh scripts/check-release.sh

## Decisions and context

Este plano decorre da auditoria de 2026-10-02, Console 0461003, registrada em docs/release-readiness.md e docs/release-readiness-evidence.md. A auditoria encontrou 28 achados, sendo 6 P0, 14 P1 e 8 P2. A solicitação de 2026-10-03 autoriza inicialização e planejamento. O usuário aprovou o plano e o commit inicial em 2026-10-03 com duas condições: TDD sempre e uso das releases mais recentes das bibliotecas, incluindo Lyra e Batuta. Este texto incorpora essas condições sem alterar a matriz ou a ordem funcional/visual/publicação.

O commit inicial autorizado registra .batuta/profile.md, .batuta/routing.md, .batuta/roadmap.md, este plano, WORK.md e os dois relatórios de auditoria, usando chore(batuta): set up console stabilization. Isso prepara o preflight sem apagar ou incorporar mudanças alheias. As tarefas de implementação seguem depois, com testes e commits por entrega.

A matriz selecionada pelo usuário em 2026-10-03 é Codex e Claude, macOS e Linux, com operação completa no web e na TUI. A TUI precisa permitir onboarding, conta/login isolado, workspace/init, configuração de squad, objetivo de missão, leitura/aprovação de plano, execução, pergunta, retomada e terminal interativo. Ambos os adapters precisam passar pelos mesmos testes de isolamento/modelo/skills e por login/jornada reais nas duas plataformas. Outros adapters e Windows ficam fora da promessa estável até qualificação própria. As arquiteturas candidatas são macOS arm64 e Linux amd64, e somente arquiteturas com evidência de runtime podem ser anunciadas.

Execução sequential, com worktrees para medium+ conforme profile e roteamento vigente. Medium vai para codex/gpt-6.1-sol, high para codex/gpt-6-astra com reasoning high, critical para o condutor self. As tarefas 10, 11, 14, 21, 28, 39 e 40 exigem o condutor e impedem tratar este plano como um loop totalmente autônomo. Antes de delegar, congelar executor/model/effort, conferir disponibilidade e seguir a escalada do routing. O inventário local de executores do desenvolvimento não certifica os adapters do produto.

Cada checkbox representa uma entrega que verifica e é commitada sozinha. Em toda mudança de comportamento aplicar TDD: primeiro escrever o caso relevante, observar a falha pelo motivo esperado, implementar e comprovar o green antes de refatorar. Registrar comandos, resultados e ordem red/green na evidência do executor e na revisão. Documentação/configuração sem novo comportamento usa provas executáveis ou revisão, sem inventar testes que espelham arquivos. Os comandos de aceitação são resultados futuros, não checks já executados. Passar uma suite sem o caso de regressão não fecha o critério.

Scripts e npm scripts são criados nas respectivas tarefas: 1 cria check-standalone.sh, 3 cria test:run e a base test:e2e, 29 cria qualify-terminal.sh, 37 cria check-release.sh/package-release.sh e 38 cria qualify-release.sh. Verificação usa ferramentas e dependências com versões registradas, nunca install latest flutuante. Assets internal/webui/dist são regenerados por npm --prefix web run build quando a UI/distribuição muda. Não editar bundles manualmente.

Não implementar um motor de execução paralelo: a autoridade de tarefas, aprovação, deliveries, journal e gates continua no core. Usar os contratos públicos efetivamente disponíveis na dependência selecionada. Durante o desenvolvimento pode ser usado um core publicado de pré-release compatível; a publicação coordenada exige uma versão estável real, fixada e testada, e versões compatíveis de host/plugin. A instalação local tinha core v1.1.0-beta.44 e plugin 0.6.9 no inventário; esses números são observações, não uma promessa de compatibilidade estável.

Documentação pública de produto continua em inglês como os docs existentes; textos de interface têm PT/EN. Este plano, roadmap e WORK são registros de condução em português. Não alterar identidade, marca ou bibliotecas de estilo/estado ao corrigir bloqueadores. A revisão da distribuição visual começa após o fechamento funcional e usará telas e sessões reais, com propostas avaliadas antes da implementação.

**Task 2.** R02 pede eliminar os seis achados da toolchain 1.26.4. A auditoria identificou 1.26.6 como correção para eles; o feed oficial consultado em 2026-10-03 aponta Go 1.27.1 como versão estável atual. Revalidar esse feed e o banco de vulnerabilidades na execução, seguindo a política de releases atuais solicitada pelo usuário. Registrar a versão realmente usada e analisar novos resultados. govulncheck deve estar instalado numa versão registrada antes da prova.

**Task 1.** Remover o replace ../core do módulo publicável. O desenvolvimento conjunto pode usar um workspace local opcional fora do artefato distribuído. A prova inclui testes/build de fonte isolada agora; instalação via tag pública só pode ser validada quando o repositório e tag existirem, na fase 3 do roadmap.

**Tasks 5–9.** Fixtures e processos são descartáveis. A recuperação após restart precisa distinguir perda de processo e processo sobrevivente sem confiar só em PID. A política concreta de ownership deve ser escolhida e registrada no brief antes de codificar. Não adicionar timeout de busy como única correção para rows abertas e transações inadequadas.

**Task 10.** Isolamento de contas é a promessa a manter. Apresentar ao condutor a allowlist de ambiente e fontes de credenciais por adapter antes de alterar código; não criar opção de fallback automático para credenciais do host. Trabalhar com chaves sintéticas, sem imprimir tokens nem testar usando as contas pessoais existentes.

**Tasks 13–16.** Qualificar Codex e Claude, os dois adapters da matriz escolhida. Consultar contratos oficiais/ajuda da CLI atual, sem adivinhar flags ou sintaxe de skills. Init confirma os campos exigidos pela skill, incluindo comandos, methodology, routing e execução, e preserva arquivos do usuário. Nenhuma conta implícita fora do catálogo isolado é selecionada.

**Task 3.** Escolher o menor runner compatível com React/TypeScript/Vite e um runner de browser para os testes de jornada. A adição dessas dependências de teste está dentro desta tarefa proposta; não adicionar bibliotecas de UI/estado. Não usar autenticação oficial nem consumo de modelo em testes de CI padrão.

**Tasks 18–21.** Migrações não podem associar automaticamente missões antigas ao delivery mais recente. Missão sem vínculo válido precisa de estado e recuperação explícitos. Aprovação deve conferir a versão exata do plano lido, antes de qualquer mudança, e preservar o contrato nativo do core. A operação de execução deve evitar duas sessões para a mesma entrega e manter uma recuperação verificável de início falhado/daemon reiniciado.

**Task 21.** O condutor confirma o contrato nativo de aprovação/execution/resume e a fronteira entre sessão de planejamento e loop. Se depender de uma capacidade ausente no core selecionado, registrar a dependência de core antes de abrir trabalho fora deste repositório. Uma versão beta instalada não substitui esse contrato.

**Tasks 22–25.** Manter o desenho existente enquanto o fluxo é corrigido. Usar polling limitado de watch como alternativa explícita se o journal não gerar eventos catalogados; websocket do catálogo por si só não é prova de progresso ao vivo. Cancelar requests/intervalos/reconnects em unmount e troca de seleção. A resposta sempre usa delivery e question_id recebidos do backend.

**Tasks 26–33.** A TUI é uma interface de operação completa por escolha explícita do usuário, sem obrigar passagem pelo web. Seu default precisa conduzir a uma missão funcional desde uma instalação vazia. Emulação de terminal e contratos do cliente precedem os formulários/fluxos. Codex e Claude precisam funcionar com foco, input e sinais corretos no macOS/Linux. Se essa qualificação falhar, registrar o bloqueador e corrigir: reduzir a TUI a monitor exige nova decisão do usuário, não uma mudança silenciosa de escopo. Usar a apresentação existente até a revisão visual da fase 2.

**Task 34.** Quotas são observações importadas ou desconhecidas enquanto não houver coletor real qualificado. Não prometer porcentagem de uso do provedor a partir do cap de sessões, nem usar timestamp da leitura como timestamp da coleta.

**Tasks 36–40.** Não existe remoto Git configurado neste checkout. Preparar workflows e bundles locais é autorizado após aprovação do plano; enviar arquivos a um remoto, criar release/tag pública e publicar não fazem parte desta fase. O destino, o versionamento do Console e a versão estável exata do core/plugin precisam estar definidos na fase 3. Compilar Linux não equivale a qualificar seu runtime. Autenticação e modelo reais precisam de conta de teste explicitamente selecionada na ocasião; não usar automaticamente as credenciais presentes no computador. Tarefas 39 e 40 registram provas no sistema operacional correspondente, sem substituir resultados reais por cross-build. Se faltar ambiente Linux ou conta de teste, registrar o requisito ainda pendente e continuar o trabalho independente.

**Task 41.** Rework continua válido até provas de fechamento. R27 permanece deliberadamente para a fase visual; os demais P2 podem ser explicitamente limitados apenas quando sua ausência não invalida uma funcionalidade anunciada e a disposição é revisada. Não encerrar o marco funcional por apenas buildar a aplicação.

**Tasks 28–29.** Esta é uma decisão técnica de protocolo/ownership, anterior ao redesenho visual. O condutor escolhe e registra uma solução de emulação com brief suficiente para a tarefa 29. Uma dependência de terminal dedicada é permitida se necessária e registrada no contrato, preservando Bubble Tea e sem adicionar biblioteca de estado/estilo. Não substituir terminal interativo por texto ANSI acumulado para fechar o aceite. Fixtures do script usam processos descartáveis; login/modelo reais ficam para a qualificação das tarefas 39 e 40.

**Tasks 30–33.** Testes do modelo Bubble Tea cobrem foco, teclas, formulário, pending, erro e cancelamento, além dos corpos enviados ao daemon. Formulários não expõem detalhes de implementação ao usuário. Eventos/progresso respeitam mission/delivery; operação da TUI usa os mesmos contratos e regras de autorização da API, com PT/EN e provas interativas nas tarefas 39 e 40.


**Task 4.** Em 2026-10-03, os registries oficiais apontam Lyra React/styles 1.1.0, e a release mais recente do core é v1.1.0-beta.46. Go @latest resolve v1.0.1 por preferir tags estáveis, portanto descobrir também pre-releases publicadas no GitHub. Consultar novamente no início da tarefa, fixar versões coerentes e qualificar migrações major. Uma restrição de compatibilidade não autoriza downgrade silencioso: corrigir a integração dentro do scope ou registrar a decisão necessária. Bibliotecas de teste novas também usam releases atuais. Não atualizar instalação pessoal de plugins/CLI como efeito colateral de atualizar dependências do projeto.

## Marcos e rastreabilidade

| Marco | Tarefas | Resultado para iniciar o seguinte |
| --- | --- | --- |
| Base, dependências e runtime | 1–16 | Build isolado, dados consistentes, processos/credenciais corretos, Codex/Claude e init recuperável |
| Jornada web | 17–25 | Plano exato, aprovação, execução, perguntas e progresso sem mistura entre missões |
| Operação TUI | 26–33 | Capabilities honestas, terminal emulado e jornada completa desde instalação vazia |
| Distribuição e qualificação | 34–41 | Quotas honestas, docs PT/EN, versões, pacote e provas reais antes da revisão visual |

| Achado | Tarefas ou disposição |
| --- | --- |
| R01 | 1, 37; instalação por tag pública na fase 3 do roadmap |
| R02 | 2, 37 |
| R03 | 20–22, 27, 33, 38–40 |
| R04 | 15–16, 31, 38–40 |
| R05 | 18, 22, 24, 32–33 |
| R06 | 19, 22, 33 |
| R07 | 10, 14 |
| R08 | 7 |
| R09 | 9, 29, 38–40 |
| R10 | 6 |
| R11 | 7–8 |
| R12 | 12 |
| R13 | 13, 32, 38–40 |
| R14 | 13–14, 26 |
| R15 | 14, 17, 26, 30, 38–40 |
| R16 | 23–24, 33 |
| R17 | 16–17, 31 |
| R18 | 26, 37–40; Windows fora da matriz selecionada |
| R19 | 36–40; remoto/tag/publicação na fase 3 |
| R20 | 27–33, 38–40; TUI com operação completa, conforme escolha do usuário |
| R21 | 34 |
| R22 | 23–24, 33 |
| R23 | 22, 25 |
| R24 | 5, 18 |
| R25 | 35–36 |
| R26 | 3, 6–19, 22–25, 27–33, 37–40; testes orientados a risco sem meta artificial de cobertura |
| R27 | Fase 2 do roadmap, após a estabilização funcional |
| R28 | 11 |
