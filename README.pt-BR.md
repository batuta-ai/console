<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/readme-header-console-dark.png">
  <img src="assets/readme-header-console-light.png" width="100%" alt="batuta-ai / console — Operação local de agentes: daemon, TUI e web. Local agent operation: daemon, TUI and web.">
</picture>

> 🇬🇧 [English version](README.md)

> *Quem rege não toca.* — The conductor does not play.

# Batuta Console

Interface local de operação para CLIs de agentes. Um daemon Go por usuário
controla sessões e uma API HTTP versionada; uma TUI Bubble Tea e uma aplicação
web em loopback são clientes desse daemon. Fechar um cliente não encerra uma sessão.

Batuta (`batuta-ai/core`) continua sendo a autoridade para entregas verificadas.
O Console mantém catálogo, recibos de aprovação e processos separados das
evidências nativas de entrega. Sessões oficiais exigem uma conta isolada
selecionada e verificações de prontidão. A injeção Batuta é solicitada por padrão;
`"batuta": false` desativa a injeção, não as proteções de conta/prontidão.
A ausência de habilidades obrigatórias de planejamento, init ou papel rejeita
a inicialização; não presuma uma sessão genérica como alternativa.

O Console depende do módulo publicado [`batuta-ai/core`](https://github.com/batuta-ai/core);
não exige checkout irmão. Não faz parte do módulo core (que continua sem daemon)
nem do pacote de plugin do host.

## Comandos

```
consoled                         start the local daemon (does not open a browser)
console                          start the daemon if needed, then the TUI
console --web                    start the daemon if needed, open the web UI, print the URL
```

`consoled` inicia o daemon local sem abrir navegador; `console` inicia o daemon
se necessário e depois a TUI; `console --web` também abre a web e imprime a URL.
O endereço padrão é `127.0.0.1:9477`. O estado fica em `$BATUTA_CONSOLE_HOME`
ou `~/.local/state/batuta/console`. `console --web` abre o navegador com
`?token=` desse diretório. Se a porta já estiver ocupada por outro diretório de
estado, o cliente termina com erro em vez de abrir uma página sem autenticação.
Passe `-home` para corresponder ao daemon em execução ou pare-o. `consoled`
nunca abre o navegador por conta própria.

## Compilar e instalar a partir do código-fonte

Use Go 1.27.2 ou mais recente e execute os comandos em um checkout da revisão
desejada do Console (o SDK qualificado aqui é Go 1.27.2). `go.mod` e `go.sum`
fixam o grafo de dependências, incluindo Batuta core `v1.1.0-beta.49`.
Os assets web incorporados e rastreados bastam para compilar ambos os binários
sem Node.js.
O Git precisa oferecer `git show-ref --exists` para a revisão nativa de missões;
os checks compartilhados verificam essa capacidade antes dos testes.

```bash
export GOWORK=off GOFLAGS=-mod=readonly GOENV=off
export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE=
go mod download
go mod verify
go test ./...
go vet ./...
go build -o bin/consoled ./cmd/consoled
go build -o bin/console ./cmd/console
go install ./cmd/consoled ./cmd/console
```

As compilações ficam em `bin/`. Mantenha ambos os executáveis: `console` procura
`consoled` no PATH e depois ao lado de si. A instalação grava os dois comandos
em `GOBIN` ou em `$(go env GOPATH)/bin` quando `GOBIN` não está definido;
adicione esse diretório ao `PATH`. As variáveis exportadas desativam workspaces
de desenvolvimento e configurações Go salvas nesse shell, forçam o modo de
módulos em vez de vendoring e ativam a verificação pública de checksums.

Para testar, compilar e instalar o código atual (incluindo alterações não
commitadas) em um diretório descartável, execute:

```bash
sh scripts/check-standalone.sh
```

Essa verificação rejeita substituições do módulo core, desativa workspaces e
flags Go de desenvolvimento e ignora o diretório vendor da raiz. Verifica o
conteúdo do cache de módulos baixados e ativa a base pública de checksums mesmo
quando desativada pelo chamador. Testes e compilações usam o grafo fixado em
modo somente leitura. Usa diretórios de saída novos e remove arquivos temporários
no sucesso ou na falha. Preserva a árvore de código e o ambiente shell do
chamador; caches comuns de módulos e compilação Go podem ser lidos e atualizados.
Módulos ou registros de checksum fora do cache exigem acesso à rede.
Esses comandos verificam a instalação a partir do código. A instalação por tag
pública do Console (`go install ...@<tag>`) aguarda publicação e verificação.
O pré-lançamento do core foi selecionado para desenvolvimento; a publicação
estável ainda exige qualificação contra um contrato estável de core/plugin.

## Jornada do operador (web e TUI)

1. **Conta.** Abra Accounts; crie uma conta Codex ou Claude com rótulo e
   selecione seu ID real no catálogo. Connect prepara o HOME privado da conta;
   não faz login. Use **Sign in** (TUI: `l`) para iniciar o login oficial da CLI
   nesse HOME. Conclua o login, volte a Accounts e atualize. A prontidão verifica
   isolamento conectado, banner exato de CLI suportada e autenticação recente
   no provedor, com verificações limitadas. Presença de arquivo de credenciais,
   rótulo conectado ou processo de login iniciado/encerrado não prova prontidão.
   Na TUI, conectar uma conta já conectada não altera nada; inspecione com `f`.
2. **Workspace.** Crie/selecione um workspace por seu ID real. Informe um
   diretório absoluto de checkout ou use o seletor nativo de pastas; se ele
   estiver indisponível, informe o diretório manualmente. O registro sozinho
   não inicializa Batuta. Revise **Stack, Methodology, Test, Build, Install,
   Execution, Worktree e Template**, notas opcionais e a conta pronta
   explicitamente selecionada. Execution é `sequential` ou `parallel`;
   Worktree é `off`, `medium+` ou `always`. Cada campo obrigatório permite até
   8192 bytes UTF-8 sem NUL nem quebras de linha; notas permitem 65536 bytes
   UTF-8 sem NUL. Confirme para iniciar o init oficial. Os valores são entradas
   literais, não comandos shell executados pelo formulário. Init pede à CLI
   que escreva `.batuta/profile.md`, `.batuta/routing.md` e `WORK.md`, preservando
   o conteúdo existente do usuário.
3. **Inspecionar configuração.** Selecione a sessão real de init para observá-la.
   A última tentativa e a prontidão da configuração são fatos separados. Um
   processo parado/encerrado não prova configuração válida. Atualize o setup
   para inspecionar a configuração em disco e o estado da tentativa. O backend
   pode reutilizar uma tentativa ativa com conta/entradas idênticas; entradas
   ativas conflitantes e setup já completo rejeitam um novo início. Uma sessão
   interrompida/unknown é histórico de catálogo, não uma PTY reconectável.
   Inspecione antes de confirmar explicitamente outra tentativa de setup.
4. **Squad e missão.** Crie ou edite um squad; selecione IDs reais de membros,
   provedores, contas correspondentes, modelos/effort e habilidades nomeadas,
   com exatamente um Maestro. Save armazena uma versão; Validate verifica
   estrutura/prontidão sem geração de modelo. Um squad salvo fora de draft
   não garante prontidão recente. Validar um draft vazio pode usar o squad
   salvo e produzir uma recusa genérica; inspecione a identidade retornada e
   mantenha o draft. Crie a missão com workspace, squad e objetivo explícitos.
   Revise o snapshot real retornado do squad: edições posteriores no squad
   não reescrevem esse snapshot da missão.
5. **Plano.** Abra a missão e escolha planejamento com o Maestro de seu snapshot.
   A CLI recebe o objetivo nos argumentos de inicialização; o Console não
   digita um comando de planejamento no terminal. Planejamento inicia uma
   sessão interativa, não execução nativa ou aprovação. Inspecione/selecione
   a sessão exata retornada e anexe-se explicitamente; observar não assume
   controle de entrada. Como alternativa, vincule um caminho exato de plano
   existente no workspace selecionado. A missão não adota automaticamente
   um plano ou journal próximo.
6. **Inspecionar e aprovar.** Antes do vínculo nativo, inspecione todos os bytes
   exatos e metadados da fonte. A aprovação registra um recibo imutável com uma
   versão SHA-256 cobrindo cada byte original. Não muda o status da fonte nem
   executa um modelo. Antes do primeiro início, qualquer mudança de byte exige
   nova inspeção/aprovação. Preserve a identidade do recibo, incluindo seu
   vínculo original com os campos de entrega/opening vazios.
7. **Executar e recuperar.** Inicie explicitamente a execução nativa a partir
   desse recibo. Exige contas selecionadas prontas, habilidades instaladas,
   rotas permitidas e uma CLI Core correspondente ao SDK vinculado
   (`v1.1.0-beta.49`). Inicie `consoled -core /absolute/path/to/batuta` para
   escolher esse executável; caso contrário, usa `batuta` no PATH. Essa
   verificação de versão/capacidade não encerra o gate separado de segurança
   do binário oficial Core. Inspecione a entrega determinística e o opening
   resultantes. Resume usa o **recibo e alvo originais e a mesma entrega/opening**.
   Após o vínculo nativo, Plan Detail retorna o **snapshot original aprovado**,
   mesmo que a fonte editável tenha mudado ou sido excluída. Os bytes editáveis
   atuais não estão disponíveis nessa API; não há alternativa de arquivo local.
   Recibos históricos continuam fatos separados e somente leitura.
8. **Perguntas e conclusão.** Responda à identidade exata da pergunta atual
   (missão, workspace, entrega, tarefa, execução, ID da pergunta e opening).
   Registrar resposta não retoma execução. Inspecione e retome explicitamente
   a entrega original. Done exige o gate de revisão atual liberado, evidências
   correspondentes e projeção atual. Saída do processo, `active=false`, contadores
   de catálogo, indicadores de atenção e volume de saída não provam conclusão.

### Controles da TUI

Use as teclas mostradas pela tela em foco. Accounts: `c`, `n` nova, Enter
para detalhes, `k` conectar, `l` login oficial, `f` inspecionar. Workspaces:
`w`, `n` novo, Enter selecionar, `e` setup, `f` inspecionar. Formulários usam
Tab para trocar campos; revise e confirme com Enter comum. Setup de workspace
usa Ctrl+R para revisão e Ctrl+A para recuperação de conta. Em campos de texto,
letras e texto colado editam o draft.

Na sala de missão: `p` planejamento, `b` vincular plano existente, `o` detalhe
da fonte, `k` recibo atual, `h` recibo histórico, `f` inspeção autoritativa,
`e` iniciar, `g` retomar, `j` resposta retida, `n` confirmar novo draft de
resposta, `w` progresso/evidências completos, `l` inspecionar ID exato de sessão.
Na fonte, Up/Down percorre páginas de bytes exatos escapados; após inspecionar
todas, `y` revisa aprovação antes do vínculo nativo. Esc sai de views/formulários
em foco. Na sala, `h` e `w` são ações locais de recibo/progresso, não navegação
global de home/workspace. Um draft respondido ou obsoleto fica retido em `j`;
saia do formulário e use `n` para confirmar explicitamente sua substituição
para a pergunta atual.

Selecione uma sessão real com `[` / `]`, `v` anexar somente leitura, `t` assumir
controle, `i` entrar na entrada do processo filho, `u` liberar, `x` parar o
processo selecionado. Controle é separado de anexação. Ctrl+] desanexa da
entrada do filho; suas teclas não são atalhos de navegação. Veja o
[comportamento do terminal](docs/tui-terminal.md) para geometria, Unicode,
replay limitado e limites de reconexão.

### Erros recuperáveis

Preserve identidades confirmadas e drafts quando uma operação falha. Timeout,
resposta perdida, resposta malformada/estrangeira ou cancelamento após dispatch
podem deixar um **efeito incerto**. Inspecione a conta/workspace/sessão real,
recibo ou entrega nativa antes de outra mutação; não repita automaticamente nem
infira sucesso de uma atualização do catálogo. Inspeção e confirmação de leitura
não provam por si que uma mutação teve sucesso. Uma operação rejeitada pode ser
corrigida e reenviada explicitamente somente quando seu efeito e alvo atual
forem compreendidos.

Para `cli_missing`/`cli_incompatible`, inspecione instalação/versão da CLI
oficial; para isolamento desconectado/inválido, inspecione o home privado da
conta selecionada; para credenciais expiradas/inválidas, use seu login oficial
e atualize a prontidão. Falhas de provedor/probe ou verificações canceladas
exigem nova inspeção, não substituição silenciosa de conta. Habilidades
selecionadas ausentes ou links de descoberta conflitantes exigem conciliar a
instalação sem sobrescrever conteúdo existente. Plano/pergunta obsoletos,
opening estrangeiro, bloqueios de revisão e entregas com proprietário exigem
inspeção de sua autoridade atual exata. Uma sessão `unknown` não pode ser
reanexada ou parada como se pertencesse a este daemon. Reconectar um terminal
é observação; não é replay de entrada, Start, Resume ou Answer.

## Infraestrutura beta

O Console usa sua própria sequência `v0.1.0-beta.N`, independente da versão
Core vinculada. A primeira versão publicada será `v0.1.0-beta.1`; a semente
`0.1.0-beta.0` do manifesto é controle interno não publicado. Nenhum release
do Console foi publicado. O repositório público está em [batuta-ai/console](https://github.com/batuta-ai/console).

Com Go 1.27.2, Node 26.10.0, npm 12.2.0 e govulncheck 1.8.0 no PATH:

```sh
sh scripts/check-release.sh       # non-root source checks; failures stop the run
sh scripts/package-release.sh     # also requires GoReleaser 2.18.2; snapshots only
```

O primeiro comando verifica o código sem root e interrompe ao falhar; o segundo
também exige GoReleaser 2.18.2 e produz apenas snapshots. Pacotes em `dist/`
contêm ambos os comandos, assets web incorporados, LICENSE e README, com
checksums SHA256, para darwin/linux em amd64/arm64. Compilações cruzadas não
são qualificação de execução nativa. Veja [setup e autoridade de release](docs/release.md)
para ferramentas fixadas, pipeline revisado de PR/tag e limites atuais de prova.
Não se afirma execução GitHub bem-sucedida nem publicação do Console. A matriz
completa de release da Task37, contrato estável Core/plugin e qualificação de
segurança do binário oficial Core CLI seguem pendentes.

## Matriz selecionada de plataformas e provedores

A matriz pretendida é **Codex e Claude em macOS, Linux e Windows nativo**.
A configuração atual de pacotes beta cobre apenas macOS/Linux. Qualificação
operacional nativa e completa de provedores, missão, navegador e TUI continua
pendente. A portabilidade de build Windows nativo (Task42) está completa;
QA de ConPTY/runtime e provedores (Tasks43–44) continua pendente;
a implementação atual de início/resize de PTY rejeita operações Windows. WSL
prova apenas Linux. Outros adapters do catálogo ficam fora dessa matriz.
Trabalho de confiabilidade segue aberto: `EPERM` esporádico de fork Linux,
ordenação de publicação de fixtures de eventos e uma falha inexplicada de teste
de resize. QA da jornada completa ainda precisa de cobertura mais forte de
PT/erro/tick/exclusão mútua, conciliação de revínculo de plano externo, duração
anormal de execução e verificações de custo de páginas da fonte. Esta documentação
não afirma que essas lacunas foram corrigidas. Instalação pelo código ou
compilação cruzada de componentes não qualifica um release.

`GET /v0/capabilities` preserva `api`, `product`, `lang`, `integrations`, a lista
de strings `adapters` e as chaves existentes de `features`. Novos objetos tipados expõem:

- `runtime`: `os`, `arch`, `go_version` do binário em execução e status de
  plataforma `selected`. `build.version` vem das informações de compilação Go,
  incluindo pseudo-versão derivada de VCS para build Git sem tag, ou `(devel)`
  quando informado pela toolchain. Versão/revisão indisponíveis são `unknown`;
  status de modificação VCS indisponível é `null`. É proveniência de build,
  não qualificação nem afirmação de que o código atual corresponde ao binário.
- `matrix`: `platforms` selecionadas (`darwin`, `linux`) e `providers` (`codex`,
  `claude`), com `qualification.status = pending` de release.
- `pty`: `supported` significa que a plataforma em execução tem o backend
  nativo selecionado. `available` significa sucesso na alocação de PTY local
  à requisição, resize e fechamento de descritor, sem iniciar processo.
  `code` é `pty_available`, `pty_unavailable`, `check_cancelled` ou
  `platform_unsupported`. O legado `features.pty` espelha `available`; é falso
  no Windows e quando a observação falha. Disponibilidade é observação pontual,
  não garantia de lançamento. Disponibilidade do backend nativo mantém a
  qualificação operacional `pending`; plataformas excluídas têm `unsupported`.
- `providers`: cada adapter do catálogo tem `id`, `cli`, `selected`, `supported`,
  seu rótulo estático de `isolation` e fatos de evidência separados.
  `installation.status` é `present`/`missing` (`cli_present`/`cli_missing`)
  pela busca de executável no PATH, ou `not_checked`/`check_cancelled`.
  Presença inclui executáveis fora da matriz e nunca prova compatibilidade
  ou prontidão. `compatibility` é `not_checked`/`isolated_account_required`
  nos pares plataforma/provedor selecionados; nos demais é `unsupported` com
  `adapter_unsupported` ou `platform_unsupported`. `compatible_banners`
  compartilha a política existente de prontidão por conta: Codex
  `codex-cli 0.160.0`; Claude `2.1.286 (Claude Code)` e `2.1.289 (Claude Code)`.
  Os banners descrevem a política, não uma versão observada.
  `account_readiness` é sempre `not_checked`/`account_readiness_required`;
  `qualification` é `pending`/`native_operation_qualification_pending` para
  pares selecionados, ou `unsupported` com o mesmo diagnóstico de exclusão.

Consultas de capacidades não inspecionam credenciais de contas, não autenticam
com provedor, não executam CLI nem alteram catálogo ou estado de conta.
Syscalls de filesystem local e PTY são síncronas; cancelamento é verificado ao
redor das observações, com recursos PTY fechados antes de retornar. Cancelamento
não interrompe syscall em andamento e não há garantia de prazo rígido para
syscalls síncronas. Diagnósticos omitem caminhos privados e erros/saídas brutos.
Consulta cancelada não implica ausência de instalação. As verificações por conta
`AccountReadiness` existentes continuam sendo a autoridade para login, init e
execução: conta isolada explicitamente conectada, verificação limitada de versão
exata da CLI e autenticação recente no provedor são requisitos separados.
Rótulos estáticos de isolamento, executáveis instalados, fixtures sintéticas de
versão e disponibilidade nativa de PTY não substituem essas proteções nem QA
completo de release. Essas observações não habilitam operações bloqueadas nem
substituem verificações por conta.

## Desenvolvimento

A UI web fica em `web/` e é incorporada de `internal/webui/dist`. Consome
[`@lyra-ds/react`](https://www.npmjs.com/package/@lyra-ds/react) e
[`@lyra-ds/styles`](https://www.npmjs.com/package/@lyra-ds/styles); tokens Batuta
sobrepõem esses pacotes em `web/src/batuta-theme.css`.

```bash
npm --prefix web ci
npm --prefix web run build
go build -o bin/consoled ./cmd/consoled
```

A diretiva Go embed exige `internal/webui/dist` ao compilar. Assets rastreados
permitem build pelo código sem Node; após editar a fonte web, regenere-os com
o build fixado Node 26.10.0/npm 12.2.0 acima. Assets incorporados ausentes impedem
a compilação: um handler de diagnóstico apenas de API não substitui um diretório
embed ausente.

Para desenvolvimento opcional contra um checkout local de core, crie um
workspace separado em um diretório pai contendo `console/` e `core/`:

```bash
go work init ./console ./core
cd console
GOWORK="$(pwd)/../go.work" go test ./...
```

Mantenha esse workspace de desenvolvimento local. Não adicione `replace` ao
`go.mod` do Console; a verificação standalone sempre usa a dependência publicada.

Connect cria um HOME isolado para aquela linha do catálogo. O login oficial
ocorre depois na PTY da sessão usando esse HOME — a API nunca aceita um segredo.

## Documentação

- [Domínio](docs/domain.md)
- [API HTTP e WebSocket](docs/api.md)

A documentação pública do repositório é em inglês, com este README irmão em
português brasileiro. Textos da UI (TUI e web) são fornecidos em inglês com um
pacote português selecionado por `BATUTA_LANG`, depois `LC_ALL` ou `LANG`.
O cliente web lê `lang` de `GET /v0/capabilities` para corresponder ao daemon,
não ao idioma do navegador.

Mudanças de idioma traduzem rótulos do produto e resumos de habilidades
oficiais/personalizadas. Nomes, papéis e propósitos de squads armazenados
(incluindo templates iniciais), texto do usuário, saída do provedor, bytes da
fonte, IDs e proveniência de recibos/evidências continuam sendo dados.

## Navegação e notas de qualificação

- [Índice de documentação em inglês](docs/README.md)
- [Assets locais de apresentação e regeneração](docs/branding.md)

O trabalho de build Windows (Task42) está completo; a execução nativa Windows
(Task43) não está qualificada e fica excluída deste branch de código. A operação
com provedores reais (Task44) e a qualificação final de release seguem pendentes.
Evidência de build não estabelece suporte operacional. A cobertura acima preserva
os limites qualificados do código original; nenhum pacote ou tag do Console
está publicado.
