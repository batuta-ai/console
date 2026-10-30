# Decisão aprovada para execução — auxiliar Core Windows

Autoridade: pedido do mantenedor “Pode rodar o loop”, em 18/10/2026, após a revisão final Opus5.5 com quatro critérios DONE e nenhum achado. O corpo abaixo é cópia exata da proposta revisada sha256 3f9f2dda9f3a90bf9ad303a0a54bb773b61b3bf1ec848df8fbcf3d8ee1557862. Seus estados de proposta documentam a versão revisada; este registro autoriza implementar os cinco passos do plano aprovado sem dispensar provas, merge ou publicação.

---

# Contrato proposto para a finalização do auxiliar Core no Windows

Estado: proposta com os nove ajustes aceitos pelo mantenedor em 18/10/2026 incorporados. O aceite autoriza ajustar o contrato e o plano proposto; a implementação ainda depende da aprovação desse plano. O plano de execução atualizado está em `../../plans/native-core-helper-finalization.md`; os artefatos congelados das rodadas anteriores permanecem intactos. Nenhuma política de produção foi modificada.

## Objetivo e limites

O Stop deve cancelar somente a tentativa nativa identificada pela missão, aprovação imutável, entrega e run nonce. Outras missões, processos e sessões permanecem protegidos. A desconexão do cliente não concede autoridade de cancelamento. Os resultados continuam apoiados em evidência do Core e no estado real do cleanup.

A mudança proposta fica no fluxo privado de execução do auxiliar Core. Não altera o SDK, o formato HTTP/DTO, os frames do protocolo, a política dos executores, os Jobs, o Store, credenciais, privilégios ou dependências. O prazo do daemon e a assertiva de cinco segundos das jornadas positivas permanecem.

## Evidência disponível

- No candidato público `78f6e69`, a suíte completa no Mac passou em 3.429 testes com `-race`. O CI de push passou nos cinco jobs, incluindo 4.039 testes Windows. O CI do PR falhou no Stop de Codex e no teste de cancelamento nativo. Uma execução verde não resolve a intermitência nem permite ignorar a execução vermelha.
- Nas duas reproduções ConPTY observadas, o mesmo relógio do pai registra expiração em cerca de dois segundos, encerramento do Job possuído e falha posterior de escrita no protocolo. O PID e a criação do hello coincidem com os birthfiles do alvo. Os journals retidos não contêm um novo terminal cancelado depois de Resume.
- O CI de diagnóstico `39017742116` também reproduziu EOF nos dois fluxos HTTP. As operações correlacionadas registram expiração, encerramento à força e EOF no header, no mesmo relógio do pai.
- Há truncamento e registros ausentes. Ausência de CoreRun ou Done na captura do auxiliar não prova que o evento nunca ocorreu. A instrumentação pode alterar o timing. Não se atribui toda falha histórica à mesma causa.
- O Core 55 usa `WithoutCancel` na finalização cancelada e lock de append com `Background`, sem um prazo total de cinco segundos. Os cinco segundos de `finishNativeExecution` são uma verificação posterior, iniciada após os defers de execução e recursos.

O Council válido terminou com REVISE: duas críticas, duas avaliações cruzadas e síntese, sem falhas operacionais; todos os 678 arquivos, inputs e diretórios ignorados ficaram intactos. O parecer não aprovou a escolha de cinco segundos. Os quinze achados foram avaliados em `native-core-cancellation-contract-council-valid-root-dispositions.md`.

Na repetição válida de 18/10, o agregado foi APPROVE, mas a síntese do presidente e o Root recomendaram REVISE. Os seis achados dessa rodada e três temas adicionais da tentativa INCOMPLETE foram julgados em `native-core-contract-council-revised-20261018-final-root-dispositions.md`. Esta revisão aplica os nove ajustes aceitos. Os pareceres anteriores avaliaram versões anteriores; não se afirma que aprovaram estes novos bytes.

## Desenho recomendado para avaliação

Adicionar uma entrada interna de Exchange com finalização cooperativa, mantendo a assinatura e o comportamento de `NativeRunner.Exchange` para compatibilidade. Hoje o auxiliar Core é o único consumidor de produção desse método; não se afirma que existe outra rota de produção independente preservada.

A nova entrada fornece cancelamento de controle imediato, um contexto de cleanup com prazo próprio e uma notificação única de fechamento após Done válido. O controle deve solicitar cancelamento sem aguardar Done ou a finalização do cleanup; a entrega do pedido continua sujeita a escalonamento e IO, sem promessa de latência zero. A notificação não cria um novo frame nem confere autoridade sobre processos. A integração só pode atuar nos handles e endpoints já possuídos.

### Relógios e precedência

| Relógio | Início | Término e responsável | Expiração |
| --- | --- | --- | --- |
| Requisição do cliente | Ação HTTP existente | Espera da requisição | Não encerra nem esquece a execução nativa por si só |
| Executor e drain de Job | Cancelamento efetivo do executor | Implementações existentes de NativeRunner e ownedProcess | Preservar interrupção de 2 s, drain de Job de 2 s e regras atuais de erros e descendentes |
| Exchange original | Cancelamento ou fechamento observados conforme o comportamento atual | Exchange existente | Preservar o contrato e provar que seu default de 2 s não recebeu a nova tolerância |
| Auxiliar cooperativo, proposto: 5 s | Primeira observação do cancelamento do contexto possuído pelo owner desse Exchange | Um único owner, até os workers e recursos exigidos terminarem | Prazo lógico fixado uma única vez; cancelar cleanup e iniciar encerramento possuído quando necessário |
| Fechamento do auxiliar, proposto: 2 s | Done passa na validação existente do handler, antes de ACK e drain | O mesmo owner, incluindo os workers do pai | Vence o primeiro prazo: fechamento ou cancelamento cooperativo |
| Verificação do Service, existente: 5 s | Depois dos defers de execução e recursos | finishNativeExecution | Só verifica o resultado; não ressuscita um auxiliar já encerrado |
| Shutdown do daemon, existente: 5 s compartilhados | Criação do drainCtx antes de native, sessões e HTTP | Daemon e lifetimes já registrados | Pode retornar incomplete antes de todos os joins; não declarar sucesso nem fechar explicitamente o Store ainda necessário |
| Jornada positiva, existente: 5 s | Antes do primeiro Stop enviado pelo teste | Teste nativo existente | Assertiva permanece; precisa de evidência própria de conclusão antecipada e margem |

Os cinco segundos propostos são um cutoff de segurança para escolha do mantenedor, não um valor já qualificado ou uma garantia de duração total. A observação do cancelamento pode ocorrer depois do pedido HTTP por escalonamento; essa diferença deve ser registrada, sem alegar uma medição atômica do instante de cancelamento. Se for exigido um limite contado do instante exato de `job.cancel`, será necessário um carrier de timestamp e uma proposta adicional de escopo.

O fechamento usa o instante de validação do Done, não o instante posterior em que uma notificação eventualmente é consumida. Atrasos de agendamento não podem reiniciar ou estender um deadline já fixado. Stop repetido, progresso, consultas e frames também não o reiniciam. Depois de ambos os prazos existirem, vale o mais cedo.

Done válido significa aceitação do envelope, identidade, sequência e resultado segundo as validações existentes do protocolo. Isso não equivale à verificação durável do journal, que continua no Service. Done e ACK não encerram a responsabilidade pelo processo, pipes ou workers restantes.

O limite de fechamento vale em TODA chamada da nova entrada, inclusive conclusão normal sem Stop. Nesse caminho, ele começa no Done validado, antes de ACK e drain; isso antecipa o início do limite em relação ao Exchange atual, cujo timer começa após o retorno do callback. Sem cancelamento ainda não existe o cutoff cooperativo, mas o limite de fechamento continua aplicável. É um delta de comportamento da finalização normal, que exige controles de sucesso e de retenção de stdout/ACK/saída sem Stop.

Esse fechamento inclui a espera pela saída raiz, retirement e drain do Job. O drain conserva seu limite próprio de dois segundos; cumpri-lo não garante concluir antes de Done+2s. Uma conclusão normal que atravesse esse deadline fica pending até os joins e depois uncertain por expiração. O prazo não é suspenso ou reiniciado pelo drain. `owned.stop()` pode aguardar o mutex que `retire` mantém durante o drain: registrar a expiração e cancelar cleanup não pode depender do retorno dessa operação. Não se promete força imediata durante retirement nem se autoriza editar ownedProcess.

### Cleanup e autoridade após cancelamento

O término do processo não desarma o deadline enquanto houver callback, guard, escritor de cancelamento, stdout ou stderr pendente. O contexto de cleanup dos guards do pai deve acompanhar esse owner; não pode continuar como um `WithoutCancel` sem prazo e sem responsável.

O sinal possuído de cancelamento de controle que arma o cutoff também torna elegível o pedido de guard Cleanup. Inclui a parada de probe solicitada pelo Manager e sua corrida de fechamento, mesmo com `job.ctx` ainda ativo. O cancelamento do callback causado somente pela saída do processo não concede essa elegibilidade. Identidade, sequência, restrições de comandos e demais autorizações continuam obrigatórias; CloseAdmission não concede nova permissão para comandos. Essa é a escolha de desenho incorporada ao documento após o aceite dos ajustes, a qual ainda será testada antes de adoção em produção.

Ao expirar, o owner cancela o cleanup, interrompe somente os endpoints de pipe que possui, quando suportado e qualificado pela plataforma, e encerra o Job possuído se ainda houver processo a encerrar. Fechamentos e erros devem ser agregados; não esconder causas independentes sob `context.Canceled`. Um processo já aposentado não concede autoridade para buscar outro PID ou atingir um processo que reutilizou seu número.

O término possuído deve ser iniciado independentemente de um Close potencialmente bloqueante. Operações assíncronas de stop e Close permanecem possuídas e entram nos joins e na agregação de resultados. O Go tenta CancelIoEx ao fechar pipes; isso não prova que os casos de leitura bloqueada serão interrompidos nessa configuração. Os testes Windows devem demonstrar o retorno das leituras de protocolo/stdout/stderr e seus joins. Não depender só de Close nem afirmar que ele nunca interrompe IO.

A expiração é uma causa independente mesmo se o cancelamento já existir. Um exit code nativo não zero também precisa manter sua proveniência na nova via, sem converter o `ExitCode=-1` publicado por cancelamento em evidência de uma saída nativa específica.

A nova via agrega um erro identificável com o código da saída nativa não zero verificada e não atribuída à terminação forçada do alvo, mesmo quando já existir erro de expiração ou outro erro de Exchange. Resultado e proveniência de força são capturados após os joins exigidos; não inferir a origem apenas pelo número do código. A saída causada pela força possuída não recebe atribuição de saída nativa espontânea. O formato do SDK e o Exchange original permanecem.

Cancelamento de contexto e fechamento de pipes não garantem interrupção instantânea de qualquer chamada de filesystem ou kernel. Os testes devem provar os casos suportados. Enquanto um worker não retornar, a execução e os recursos dos quais depende continuam registrados. Se o escopo permitido não conseguir conservar essa responsabilidade ou tornar um caso exigido interrompível, a implementação deve parar com a limitação concreta; não declarar conclusão limitada em walltime.

`internal/service/native_run.go` permanece somente de leitura. Seu mutex linkMu no guard, a chamada ao guard após Exchange e AppendEvent antes da finalização podem esperar operações que não respeitam o contexto. São casos concretos de trabalho ainda registrado e pending, sem limite físico prometido. Se uma prova exigida depender de torná-los interrompíveis fora dos cinco arquivos de produção, apresentar a ampliação concreta antes de editar. O stub `internal/service/native_core_other.go` e o entrypoint `cmd/consoled/native_core_windows.go` também são referências somente de leitura.

### Resultados observáveis

| Condição | Resultado permitido |
| --- | --- |
| Execução ainda com cleanup, verificação ou joins pendentes | `202 pending`, sem afirmar finalização ou durabilidade |
| Entrega aberta: recursos, terminal, identidade e gate foram verificados conforme as regras atuais; não existe erro independente, e o run terminou sem erro ou apenas com cancelamento esperado | `200 completed` |
| Trabalho de finalização terminou, mas houve expiração, falha de protocolo, cleanup ou outra causa independente | `503 uncertain`, com todas as causas preservadas |
| Terminal durável verificado, mas o auxiliar ficou preso após Done/ACK e precisou de força por expiração | `503 uncertain`; a evidência verificada pode permanecer `Durable=true` |
| Stop antes da abertura, com todos os recursos exigidos terminados e sem expiração nem erro independente | `200 completed` quando a ausência de evidência e CoreState for verificada conforme a regra atual; não exigir um terminal que nunca foi aberto nem inventar um journal |
| Expiração da nova via antes da abertura, mesmo sem journal | Após a finalização exigida, `503 uncertain`; a causa independente de expiração vence a exceção de ausência de evidência |
| Terminal `done` durável, mas gate de revisão exigido ainda não liberado | Preservar `uncertain`, podendo manter `Durable=true`; durabilidade não substitui a revisão |

A expiração do auxiliar não implica `503 finalized` imediato se ainda existirem joins pendentes. Nesse intervalo, a execução continua pending. O resultado final incerto só é publicado após a finalização exigida pelo contrato existente. Não prometer eventual `200` para um journal mantido bloqueado.

O delta de API é de comportamento, não de formato: a fase pending pode durar mais durante cooperação; expiração continua explícita como causa de incerteza mesmo se houver evidência durável. A finalização normal sem Stop também passa a ter o fechamento limitado a partir de Done, em vez de esperar o callback retornar para iniciar o timer; falhas desse caminho devem manter sua proveniência no resultado existente da execução. As validações de referência, autorização, desconexão e tentativa obsoleta permanecem.

### Shutdown

CloseAdmission mantém o comportamento atual de impedir novas admissões e não concede permissão para iniciar operações de cleanup que hoje seriam recusadas. ShutdownNative cancela os jobs admitidos; Manager.Shutdown pede Stop aos probes e acompanha o trabalho ativo. Um pedido de encerramento desse caminho usa o mesmo owner e não cria outra janela reiniciável.

O daemon conserva seu prazo compartilhado de cinco segundos. Ele pode terminar a espera com shutdown incomplete antes do fim do cutoff do auxiliar, do drain de Job ou da verificação. Até a saída efetiva do daemon, operações e dependências ainda ativas não podem ser esquecidas ou receber fechamento explícito prematuro do Store. Não se promete a execução de finalizadores depois que o processo do daemon termina.

A qualificação precisa exercitar o daemon real, sua saída e o destino dos processos possuídos; o drain da fixture com `context.Background()` não substitui essa prova. Se uma saída incomplete deixar um auxiliar ou recurso órfão, a beta continua bloqueada e será necessária uma proposta específica, sem aumentar silenciosamente o prazo do daemon.

O novo teste Windows executa o daemon construído pelo entrypoint real `main`, em um grupo novo e próprio com CREATE_NEW_PROCESS_GROUP. Um controlador de teste compartilha o console e usa GenerateConsoleCtrlEvent com CTRL_BREAK_EVENT dirigido somente ao grupo criado, após readiness observada. Não usar broadcast, SIGTERM no Windows ou fechamento da janela. Observar entrada em shutdown, incomplete quando esperado, saída do daemon e destino dos filhos, incluindo KILL_ON_JOB_CLOSE ao fechar o último handle do Job. O teste retém o handle de criação do daemon. Para os descendentes criados pelo daemon, abre handles witness somente de observação enquanto vivos e verifica PID/criação contra os birthfiles da fixture; esses handles não concedem autoridade de término. Falha de entrega do sinal ou de observação torna a qualificação falha ou inconclusiva, sem skip. Demonstrar viabilidade no ator PRIMARY limitado; uma necessidade de trigger de produção, edição de main ou privilégio adicional exige proposta concreta antes de ampliar. Os defers de runDaemon executam antes de seu retorno a main e de eventual log.Fatal.

## Orçamento de tempo e casos positivos

O cutoff cooperativo engloba o encerramento do executor, cleanup do Core, fechamento e saída do auxiliar. O intervalo de journal bloqueado pode se sobrepor ao encerramento do executor. Somar todos os limites como fases consecutivas não descreve a execução.

Por outro lado, o executor e seu drain podem consumir quase toda a janela proposta, e o lock do journal não tem limite total. Portanto nenhum cutoff finito garante sucesso com qualquer atraso de IO. Consumir cinco segundos depois de um cancelamento observado posteriormente também não cabe, por definição, numa jornada de cinco segundos iniciada antes do pedido. São limites com objetivos diferentes.

O positivo exige uma programação de liberação identificada e medida: intervalo hold/release, pedido HTTP, observação de cancelamento, Done/ACK e joins no mesmo relógio quando possível. O journal precisa ser liberado cedo o bastante para a conclusão real ocorrer antes do deadline original, com margem registrada. As primeiras medidas definem o intervalo de hold coberto pela qualificação; até lá não há duração máxima de hold garantida pelo produto. Uma retenção atravessando o cutoff é um negativo incerto, não uma promessa de recuperação bem-sucedida.

## Base e lista fechada de arquivos

- Base pública: `78f6e6916f5f22b74deda3399f7c17ab428d7ceb`, 671 arquivos rastreados.
- Fixture limpa: `8d20963badd11695f38b8faa96af02da81ee0b7b`, 673 arquivos. Acrescenta dois testes Windows e o observer de Stop no host existente, usando os três blobs de `6e9d545`.
- A única diferença entre 8d20963 e 6e9d545 é um teste exclusivo de Unix. Os inputs compilados Windows são iguais. A prova de 6e é uma referência por equivalência, não uma nova execução nativa em 8d.
- As jornadas positivas dessa fixture continuam sem aprovação. Código e workflows de diagnóstico permanecem fora da entrega.

Produção proposta:

- `internal/session/native_exchange.go`
- `internal/session/native_exchange_windows.go`
- `internal/session/native_exchange_other.go`
- `internal/service/native_core_windows.go`
- `internal/service/native_core_protocol_windows.go`

A entrada interna é aditiva; o método Exchange original conserva sua assinatura. O bridge e o stub da plataforma estão incluídos. Não se propõe campo em NativeRunner, arquivo de produção adicional ou mudança em ownedProcess, Manager, daemon, Store ou SDK. Os dois arquivos novos de teste estão enumerados abaixo; esta proposta ainda não autoriza criar código. Uma necessidade adicional requer descrição concreta antes de ampliar.

Testes e fixture propostos:

- `internal/session/native_exchange_windows_test.go` — controles de Exchange e ponte de observação compilada somente nos testes
- **Novo:** `internal/session/native_core_service_contract_windows_test.go` — pacote externo session_test, API pública do Service e contraprova do owner usando a ponte de testes
- `internal/service/native_core_service_windows_test.go` — helper controlado, reutilizando o caminho real de Core/Done
- `internal/service/native_core_windows_test.go`
- `internal/service/native_stop_windows_test.go`
- `internal/tui/public_flow_host_windows_test.go`
- `internal/tui/public_flow_http_windows_test.go`
- `internal/tui/public_flow_stop_observer_windows_test.go`
- `internal/tui/outer_native_stop_journey_windows_test.go`
- `cmd/consoled/native_test.go` — controles existentes do drain no próprio processo
- **Novo:** `cmd/consoled/shutdown_windows_test.go` — entrypoint real do daemon no Windows, saída, handles de processo e destino dos filhos; o `shutdown_test.go` existente é exclusivo de Darwin/Linux e permanece como regressão somente de leitura

Documentação proposta: `docs/session-lifecycle.md` e `docs/api.md`. Os módulos de observação, flags privadas e seus testes não entram no candidato de entrega. Os demais arquivos são superfícies de regressão somente de leitura.

Referências de avaliação somente de leitura incluem explicitamente `internal/service/native_run.go`, `internal/service/native_core_other.go`, `cmd/consoled/native_core_windows.go` e `internal/service/native_shutdown_test.go`, além dos relógios de Service/Manager/Jobs e do Core publicado já citados. `native_shutdown_test.go` permanece exclusivamente como regressão Darwin/Linux conforme sua build tag; não recebe prova nem edição para o auxiliar Windows. A aceitação da avaliação deve julgar origens e precedência dos relógios, identidade dos erros de expiração, elegibilidade e autorização de cleanup, fronteira pending/finalized, viabilidade dos cinco arquivos de produção e gatilho/observador de cada prova, separando escolhas de desenho de evidência futura.

### Observação causal restrita aos testes

A contraprova é planejada no pacote externo session_test, em `internal/session/native_core_service_contract_windows_test.go`. Ele importa Service e session; uma ponte de teste exportada apenas por `native_exchange_windows_test.go` lê o Manager e os objetos possuídos daquele processo de teste. O código normal de session não importa Service. O teste constrói Service pela API pública, usa o daemon real como NativeExecutorExecutable e provedores sintéticos controlados. Não chama a função privada runNativeWindowsExecution como substituto da API pública.

Os helpers em arquivos de teste de package service são privados daquele pacote e não são importáveis pelo novo session_test. O novo arquivo deve conter sua própria fixture mínima: TestMain com despacho dos PEs dos provedores sintéticos, build do daemon real, Store, contas/skills, criação de missão e aprovação pela API pública, hold/release do journal e birthfiles. PEs de fixture são permitidos somente como provedores sintéticos; o auxiliar executado por NativeExecutorExecutable é sempre o daemon real construído da fonte exata. Compartilhar um pacote novo de helpers, mover código para produção ou editar os testes Service para expor helpers a esse pacote exige proposta concreta antes de ampliar. Não duplicar o protocolo privado Core: ele continua sendo executado pelo daemon real.

A ponte associa o alvo usando o handle de processo já possuído, PID e criação verificados contra o birthfile da fixture e a tentativa imutável conhecida pelo teste. Zero ou múltiplos alvos é falha. Ela conserva uma referência ao mesmo objeto possuído e duplica um handle apenas para observar saída; não envia Stop, não enumera PIDs para obter autoridade e não modifica clocks ou decisões. Depois dos joins, o RED exige forced e signaled observados no owner correto, retirement concluído sem erro de término, saída pelo handle retido e falha de protocolo/ausência de Done e CoreState aceitos. O GREEN exige ausência de força no mesmo alvo, terminal sincronizado, Done/ACK e joins reais. Captura parcial ou erro de observação torna a prova inconclusiva.

Hold/release, chamada Stop, amostras de força e saída usam o relógio monotônico do teste. Horário de amostragem não é o instante exato da decisão de força. O contexto local do Exchange antigo não é exposto pela ponte: registrar o instante da primeira observação do cancelamento pelo owner antigo como não observável, sem inventar precisão. A condição controlada maior que dois segundos, o estado forced/signaled do objeto real e a identidade/saída observadas discriminam o mecanismo; um simples 503 ou uma duração aproximada não discrimina.

Essa ponte não existe ainda. Antes de usá-la como evidência, a implementação deve demonstrar que a variante Go test permite session_test importar Service junto com a ponte de session, sem ciclo, e que a leitura dos estados é sincronizada e não acrescenta autoridade. Um teste de controle sem parada deve observar ausência de força, e outro com força possuída deliberada deve observar a presença. Manter o mesmo código de teste e de ponte nas fontes antiga e nova. Se a arquitetura não compilar ou exigir hook de produção fora do escopo, apresentar a limitação concreta; não substituir silenciosamente por reflexão, unsafe, linkname, overlay ou o diagnóstico NEVER-SHIP. A qualificação posterior executa também o candidato sem essa observação de testes.

## Provas exigidas antes de fechar a mudança

1. Validar primeiro os controles da ponte somente de teste descrita acima. Fixture de Core real, controlada por evento, com finalização maior que dois segundos e menor que o cutoff proposto. Os mesmos bytes de teste e ponte, pela API existente do Service, devem falhar no contrato antigo especificamente por força prematura observada no owner correto e passar no novo por terminal sincronizado, Done/ACK e todos os joins, sem força. Correlacionar hold/Stop/amostras/saída e explicitar timestamps indisponíveis; 503, EOF, cancelamento, ausência de logs ou duração isolados não contam como RED causal. Falha de compilação não conta como RED.
2. Contraprova que discrimine o default de dois segundos do Exchange original da janela do auxiliar. Exercitar também conclusão normal sem Stop: sucesso completo antes do limite contado de Done e expiração independente quando ACK, stdout ou saída ultrapassarem esse limite, mesmo antes de o callback retornar. Incluir saída raiz mais drain bem-sucedido atravessando Done+2s, com pending até joins e uncertain depois, e stop/Close bloqueantes que não impeçam contabilizar expiração. O watchdog existente de seis segundos, isoladamente, não basta. Manter provas independentes dos executores e Jobs.
3. Auxiliar sem Done; auxiliar com terminal real, Done/ACK válidos, stdout já fechado e callback já retornado, mas processo ainda travado antes de sair. Esse caso deve isolar a espera pela saída e não pode ser substituído pelo caso distinto de stdout retido. Cobrir também guard/callback após processjoin e falhas independentes de ACK, pipe, stderr, saída nativa e persistência. Expiração após evidência válida precisa permanecer incerta, preservando a durabilidade já verificada.
4. Stop repetido e progresso sem reset de deadline; corrida de conclusão/expiração; contexto já cancelado sem novo lançamento; cancelamento limpo antes de abertura e, separadamente, expiração antes de abertura com incerteza explícita; Manager cancelando o controle antes de job.ctx e corrida de fechamento, com elegibilidade sem nova autorização de comandos; preservação de irmão e processo estrangeiro; cliente desconectado sem perda de ownership. Uma conclusão selecionada tardiamente não pode apagar expiração já atingida; o tratamento deve ser conservador e testado. Controles pareados devem conservar expiração mais saída nativa não zero genuína e evitar atribuir saída nativa espontânea à força possuída.
5. Em `cmd/consoled/shutdown_windows_test.go`, executar o entrypoint real com journal retido e Stop concorrente, incluindo incomplete, saída do daemon, recursos e destino dos processos. Não usar apenas o drain irrestrito da fixture.
6. Jornadas HTTP e ConPTY completas dos dois provedores, positivos e fault, no Windows `server.lynx-kelvin.ts.net` com o desktop PRIMARY limitado, cinco privilégios, sem Admin/Backup/Restore/Symlink habilitados. Usar Go 1.27.2, CGO, race, GOMAXPROCS=4 e manifestos de ferramentas/fontes e ator antes/depois. Preservar a assertiva original de cinco segundos.
7. Revisão independente Opus 5.5 do candidato exato. A aceitação local cobre os testes nomeados e a comparação de regressões. Merge e beta exigem todos os checks finais verdes; falhas de Take, NativeConsole ou lifecycle sem explicação não são dispensadas. Contas reais, pacotes, matriz de plataformas, Council final e instalação continuam como gates próprios.

Esta revisão incorpora os ajustes aceitos e atualiza o plano ainda proposto. Não qualifica cinco segundos como parâmetro de produção nem implementa a entrada interna. A implementação depende de aprovação do plano e de suas provas.

Referências das afirmações de evidência: a execução original do Mac está em `native-stop-pending-answer-integrated-root-full-go/`. Sua cópia de revisão está em `../../worktrees/native-helper-contract-review/.batuta/runs/contract-review-proof/Mac-full-Go-78f6e69/`, com `root-receipt.json`, `tests.jsonl` e `source-before.json` (hash por arquivo). Nesse mesmo pacote, `evidence-index.json` associa os nomes às afirmações; `official-run-event-metadata.json` identifica os eventos push/PR; `native-core-cancel-observation-ci-root-source-proof.json` nomeia o run39017742116 e `native-core-cancel-observation-ci-http-eof-phase-proof.json` contém as fases correlacionadas. As cópias ficaram intactas durante a revisão independente.
