# Plan — Localizar as operações anteriores ao terminal e preparar a nova prova pública
<!-- inputs: profile.md@sha256:4541669f7097 routing.md@sha256:c3d5dd7cafa7 -->

**Goal:** Medir quais operações consomem o intervalo anterior ao registro terminal, preservando cancelamento, autorização e recuperação do trabalho. Produzir uma proposta de correção ou outra fronteira de investigação sustentada pelos dados, antes de voltar à prova pública de Stop.
**Created:** 2026-10-25 · **Status:** proposed

## Tasks

- [ ] 1. Fechar o contrato do perfil por operação e seu diff proposto — testing/critical
      Scope: .batuta/runs/windows-preterminal-command-profile/design/**
      Accept: Mapa somente leitura liga nativeCoreCommands.Run, guard RPC, NativeRunner.Run, recordFailureWithPolicy/snapshotWorktree e finish/recordFinalization às fontes exatas de Console 48410e8b e Core 55, sem atribuir duração ainda não observada; proposta de diff identifica o transporte privado da configuração e dos registros até o helper real e os seis caminhos da tarefa 2, sem aplicar código, acessar credenciais ou trocar SDK; contrato fixa em design/fork.json o repositório de condução, branch batuta/native-core-command-profile-<nonce>, worktree .batuta/worktrees/native-core-command-profile-<nonce> e base 48410e8b564a54b73287d42b39c502d429c4df1f, com nonce resolvido antes da decisão e Scope relativo àquela worktree; contrato fixa antes da tarefa 2 o limite de perturbação de 100 ms e a comparação conservadora definida abaixo, sem derivá-lo de dados do novo experimento; contrato especifica relógio monotônico local do helper, pareamento begin/end por operação, vínculo ao RunID/PID/birth e ao artefato real, limite de 100000 registros/16 MiB, detecção de perda e recolhimento dos produtores, sem gravar no journal nem atrasar a liberação; Council somente leitura e julgamento de achados precedem a decisão sobre o diff, e nenhuma escrita da tarefa 2 começa antes dessa decisão.

- [ ] 2. Implementar e verificar o observador somente no fork diagnóstico — testing/high
      Depends on: 1
      Scope: internal/service/native_core_windows.go, internal/service/native_core_protocol_windows.go, internal/service/native_core_command_profile_windows.go, internal/service/native_core_command_profile_windows_test.go, internal/session/native_core_service_contract_windows_test.go, internal/session/native_exchange_windows_test.go, .batuta/runs/windows-preterminal-command-profile/implementation/**
      Accept: Somente o diff da tarefa 1 aceito pelo mantenedor é aplicado no fork fixado em design/fork.json, com seus seis paths Go de Scope resolvidos relativamente àquela worktree; antes de qualquer escrita Root verifica repo/branch/worktree/base e o hash do descriptor, HEAD descendente de 48410e8b564a54b73287d42b39c502d429c4df1f e não descendente de 856bb75e3311fd0ef60cd087e64de589df1a4319, e recusa cwd na branch V3/documentação/primary; o wrapper chama os runners e guards reais, conservando executable/argv/env/contexto/autoridade/resultado/erro e sem reproduzir backend ou dispensar autorização; unidades discriminantes provam pareamento e preservação em sucesso/cancelamento/erro, limites e perda explícita, ausência de espera em owned.mu/journal/release e responsabilidade pelos produtores; saída parcial, inclusive helper forçado antes de concluir o perfil, invalida o perfil em vez de inventar duração ou omitir registros; fonte, PE, configuração, ferramenta, pipeline de captura e revisão independente são fixados antes da validação nativa, sem SDK replace, mudança em Core, GORACE, prazo, hold ou API pública.

- [ ] 3. Qualificar o observador e executar uma campanha finita — testing/critical
      Depends on: 2
      Scope: .batuta/runs/windows-preterminal-command-profile/native/**
      Accept: Dois recipes previamente declarados do mesmo snapshot/binário, observação desabilitada e habilitada, recebem exatamente dois ownercontrols novos por receita e um smoke por arm NoStop/StopNoHold/StopHeld, teto total quatro folhas de owner e seis smoke, sem terceira receita ou substituição e com parada na primeira falha; pares de smoke só dão evidência finita de perturbação, e divergência funcional ou alteração de intervalos que não seja compatível com o limite previamente fixado de 100 ms encerra a qualificação como efeito do observador não resolvido; após os controles válidos, uma única campanha habilitada de nove labels em três rodadas fixas preserva a captura pública P/E/R/J/F, os bytes/seq/digests e os novos spans por operação, com incerteza/censura explícitas; durações locais do helper não são subtraídas de timestamps do pai, e a associação entre spans e a janela pública precisa de ordenação causal/identidade comprovadas; causa desconhecida, perda de dados, identidade/protocolo inválidos, controle NoStop anormal ou join não resolvido abortam o batch, enquanto uncertain/expiry/force conhecidos continuam dados funcionais negativos, sem repescagem.

- [ ] 4. Propor a correção mínima e a rota pública conforme o resultado — testing/critical
      Depends on: 3
      Scope: .batuta/plans/windows-preterminal-fix-proposal.md, WORK.md, .batuta/runs/windows-preterminal-command-profile/assessment/**
      Accept: Relatório distingue tempo no guard, runner de comandos e intervalo ainda não atribuído, sem somar spans sobrepostos ou chamar correlação de causa; se o perfil não discriminar, declara inconclusão e propõe o próximo recorte com paths e orçamento, sem otimização por palpite; se discriminar, prepara uma proposta de correção com regressão causal e preservação de autorização, parked refs, snapshot/recuperação e durabilidade, sujeita a Council e decisão antes de implementação; nenhuma conclusão desta investigação satisfaz a antiga Task 1 pública nem autoriza integração, publicação ou instalação, e a futura proposta conserva integralmente os gates abaixo.

## Decisions and context

Esta é uma proposta de investigação seguinte. A tarefa 6 do plano anterior autoriza escrever esta avaliação, sem executar o observador ou modificar Console/Core. O passo 1 prepara um contrato e um diff revisáveis, necessários porque ainda não existe um canal de perfil qualificado no helper real. A tarefa 2 depende da decisão sobre esse resultado concreto. Não há aprovação implícita para ampliar caminhos, injetar telemetria em produção ou publicar um SDK.

O documento foi preparado na branch batuta/native-beta-blockers-assessment-a08e1a70, derivada do V3 verificado 856bb75e3311fd0ef60cd087e64de589df1a4319, tree 118d9860ad8687138119da42f277d3c02f9d3096. A entrega fica em V3. Um eventual experimento nasce em fork de 48410e8b564a54b73287d42b39c502d429c4df1f, a mesma origem diagnóstica independente de V0 usada na campanha. Código desse fork não é promovido à entrega. Evidência de uma árvore não qualifica outra.

### Evidência concluída

| Etapa | Fonte verificada | Prova e limite |
| --- | --- | --- |
| Registro privado | V1 a54d80045045ee07e739a10b1350fb659f570b1d | 119 PASS nativos, três mutantes com falhas causais, Opus5.5/5DONE. Contagem e origem do prazo, sem promessa de kernel IO. |
| Callback real antes de ACK | V2 bd46312fbfe965ed3cac1fa2da762cb185d690c2 | 78 PASS, quatro mutantes/14 falhas causais e quatro controles PASS, Opus5.5/7DONE. Extração literal preserva o relatório e o cancel writer. |
| Operações de finalização | V3 856bb75e3311fd0ef60cd087e64de589df1a4319 | 167 PASS, três mutantes/17 falhas causais e três controles PASS, Opus5.5/7DONE. Agendamento, pendência e joins privados, sem prova de interrupção física de Close. |
| Harness diagnóstico | 48410e8b, somente dois arquivos de teste sobre V0 | Candidato operacional3, bootstrap PRIMARY5 com 11 diretórios privados, dois ownercontrols novos e três smoke válidos. Os dois candidatos anteriores passaram seus dois controles de owner cada e depois falharam no setup do smoke. Os lotes continuam preservados. |
| Campanha externa | Mesma fonte e binários do harness | Nove amostras/nove RunIDs, três rodadas, nenhuma substituição. Captura e resultados funcionais verificados, receita de 21 arquivos intacta. |

Nos três controles NoStop houve saída natural 0, estado waiting_input, terminal sincronizado e retirada do owner sem força. Os três Stop sem retenção concluíram sem força. Os três Stop com retenção terminaram uncertain, com expiração e força, apesar do estado cancelado durável. Esses três são resultados negativos do produto; captura válida não significa aprovação para a beta.

Binário de teste/provedor sintético: 397df50dd4d6fc6db59d6fdb75f95d1a9b872fe4a8de666dc2ff1ede58d6a7c4. Helper real: c5d5f79a86fb2472e36034d1faaebda59d671197a7c3ce7aa7366b84541327d5. Todos os runs usaram Go1.27.2-race, CGO1, Core v1.1.0-beta.55 sem replace, quatro CPUs e o usuário comum PRIMARY5. Não foram concedidos privilégios nem alteradas ACLs globais. A correção operacional cria as DACLs privadas no ato e não reaplica Set-Acl.

### Resultado temporal

P é a observação de saída do provedor, E a do helper, R a liberação da retenção, J a primeira presença completa do terminal no journal e F_stop a primeira resposta pública finalizada. Os eventos são intervalos do relógio monotônico do pai, não instantes exatos. Envoltórias conservadoras das três rodadas, arredondadas para fora:

| Intervalo | Envoltória observada |
| --- | --- |
| P→J, Stop sem retenção | 1,796–1,846s |
| R→J, Stop com retenção | 1,784–1,812s |
| J→E, controle NoStop | 1,031–1,094s |
| J→E, Stop sem retenção | 1,000–1,061s |
| E→F_stop, Stop sem retenção | 0,020–0,078s |
| J→E, Stop com retenção | 0,453–0,501s, truncado por força |

Todas as três rodadas satisfizeram as regras fixadas antes da campanha: limite inferior preterminal ≥ 500 ms e maior que os limites superiores das caudas relevantes e do controle normal, com o provedor já observado como encerrado antes de R. Root conferiu os cálculos independentemente do classificador e das durações relatadas pelo teste Go. Resultado: apoio à localização temporal do atraso anterior ao terminal. A causa continua não identificada.

Não interpretar a cauda forçada como saída natural ou custo de race. A consulta passiva ExecutionState do NoStop pode disputar journal/Git; o resultado se refere a essa carga de observação. F_registry_noStop mede retirada pública do registry, sem equivalência com F_stop ou com o retorno literal da goroutine. NoStop contribui ao teste da hipótese somente com J→E. J demonstra visibilidade completa, não o momento do Sync. A confirmação final sincronizada e a durabilidade foram verificadas separadamente.

### Próxima fronteira sustentada pela leitura de código

No Console, internal/service/native_core_windows.go contém nativeCoreCommands.Run: ele valida a autoridade, pode aguardar rpc.call(...,"guard",...) e depois chama o NativeRunner real. É um ponto concreto para medir separadamente guard e runner sem copiar o backend. O desenho precisa primeiro demonstrar como levar configuração e registros privados ao helper real sem desviar a admissão ou o protocolo.

No Core 55 publicado, loop/attempt.go chama snapshotWorktree no tratamento de interrupção, antes de escrever a falha. loop/report.go:finish ainda executa preservação/listagem de refs antes do registro terminal; loop/runner.go:runOwned chama esse caminho com contexto de limpeza. Isso estabelece operações candidatas e obrigações de recuperação. Não demonstra que qualquer uma delas cause os 1,8s. Ler executor/backend.go, executor/run.go e executor/transport.go completa o mapa de retorno do executor. Esses arquivos de Core são somente leitura nesta proposta.

Não retirar snapshot, parked refs, validação de guard, flush ou joins para ganhar tempo. O caminho já demonstrado ctx→CLIBackend→Subprocess→NativeRunner→owned.stop/TerminateJobObject continua refutando a antiga justificativa de economizar saída normal do provedor usando um build sem race. Esse experimento não será retomado.

A execução futura é sequencial e conduzida por Root. Não iniciar um loop genérico a partir desta branch de documentação: os paths Go da tarefa 2 pertencem exclusivamente ao fork descrito em design/fork.json. O descriptor deve resolver o nonce e os caminhos absolutos no workspace antes de a tarefa 2 ser liberada. Mudança de identidade exige nova decisão, sem reaproveitar a aprovação de outro checkout. O descriptor também fixa conductor_checkout e artifact_root. Todos os artefatos design/**, implementation/**, native/** e assessment/** de .batuta/runs/windows-preterminal-command-profile/ pertencem ao checkout de condução, sob artifact_root. Apenas os seis paths Go da tarefa 2 pertencem à source_worktree diagnóstica. Root verifica ambos os roots e passa os destinos absolutos no brief, sem resolver artefatos relativos ao cwd do executor.

O limite de 100 ms para perturbação é uma regra diagnóstica nova proposta, adotada da resolução máxima de observação já usada; não é uma estimativa estatística de overhead nem um resultado desta campanha. A tarefa 1 fixa sua definição antes da implementação. Para cada duração necessária e arm, a diferença habilitado−desabilitado usa [L_on−U_off, U_on−L_off]. Só há compatibilidade se o intervalo inteiro estiver dentro de [−100,+100] ms, com captura admissível e resultado funcional compatível. NoStop compara P→J e J→E; StopNoHold compara P→J, J→E e E→F_stop; StopHeld compara R→J, J→E e E→F_stop. Intervalo ausente, incerteza excessiva ou diferença não demonstrada dentro do limite é inconclusão sobre o efeito do observador e impede a campanha, sem nova amostra ou ajuste do limite. É uma evidência finita por par, não prova universal. Não confundir esse limite com o limiar temporal de 500 ms da hipótese anterior.

**Task 1.** O diff proposto deve escolher e documentar um transporte que funcione no processo real, não apenas no teste pai. Os registros devem identificar categoria de comando e estágios guard/runner; dados de credenciais, prompts e saídas de comandos não entram no perfil. O buffer não pode esperar no journal, em owned.mu ou na goroutine de release. Falta de selo final, registro parcial, perda ou impossibilidade de recolher produtores invalida o perfil. Se os seis paths não bastarem, preparar proposta explícita de escopo adicional; não ampliar durante implementação.

**Tasks 2–3.** As duas receitas e os seis smoke são uma qualificação finita do efeito do observador, não prova universal de overhead nulo. É uma autorização nova a decidir, não sobra do orçamento anterior: o plano concluído iniciou três candidatos operacionais, executou seis folhas de owner, todas PASS, e reservou cinco tentativas de smoke. Houve quatro invocações Go de smoke: uma falha de setup histórica e três válidas. A outra tentativa terminou antes de executar Go. Os ledgers task4/native-run-ledger.json, task4/candidate2/native-run-ledger.json e task4/candidate3-5de08d05/native-run-ledger.json, no checkout de condução, registram reservas e tentativas. A distinção entre Go executado e falha anterior ao teste está em task4/candidate1-root-disposition.json e task4/candidate2/smoke-v2-noStop-1ef90d929b504b22a6a7f434ac5bc9e6/failure-recovery.json. A primeira falha encerra o novo experimento. Nada substitui a campanha anterior nem muda seus critérios ou conclusão. A campanha seguinte só começa depois de congelar o novo snapshot, os dois modos, binários, seleção e regras de associação dos spans. Não subtrair timestamps de processos distintos.

### Gates públicos preservados

A antiga tarefa 1 e o delivery native-core-helper-finalization-20261018-152521 continuam BLOCKED, sem reset, replay ou integração. Encerrar esta investigação não satisfaz as dependências das tarefas 6–8 do plano de recuperação de 18/10/2026, que não são as tarefas deste documento.

Uma futura rota de correção precisa conservar a mesma fixture pública, hold de 2750 ms, jornada de 5 s desde cancelamento e prazo de 2 s desde Done validado, primeira origem sem reset. Precisa de três pares completos antigo 34d RED causal/candidato GREEN, até 4,5 s e margem conservadora mínima de 500 ms, sem força/erro e com estado cancelado, durabilidade e todos os joins. Setup/buildfail não é RED. Resultado inconclusivo, antigo PASS ou falha do candidato encerra aquela tentativa, sem tuning de hold, deadline, GORACE ou escolha de amostras.

Continuam pendentes as lacunas de interrupção nativa de IO/Close e drain, a alternativa de retenção somente de processo ainda não qualificada, negativos e shutdown do daemon real, HTTP/ConPTY dos dois provedores e preservação de irmãos/estrangeiros. A fonte final ainda precisa de full Windows ordinary e race pertinentes, Mac-race, web/TypeScript, revisão formal Opus5.5 exato com saída/guards reais, CI, Council final, contas locais, pacotes, publicação autorizada e instalação no Mac. Fallback automático para outro modelo não satisfaz o gate formal de Opus5.5.

### Evidências e retomada

Os recibos locais estão no checkout de condução em .batuta/runs/2026-10-24-windows-beta-blockers/: task1/root-final-receipt.json, task2/root-final-receipt.json, task3/root-final-receipt.json, task4/candidate3-5de08d05/root-native-smoke-receipt.json, task5/root-final-receipt.json, task5/temporal-assessment.json e os nove lotes apontados pelo ledger. São artefatos locais preservados, não arquivos de produto publicados. O registro assessment/isolation.json liga esta documentação ao V3. Não há implementação, release ou merge autorizado por este documento proposto.
