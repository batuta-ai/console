# Roadmap — Batuta Console para a versão estável

- [ ] 1. Corrigir bloqueadores e completar o fluxo funcional → plans/estabilizar-console.md
- [ ] 2. Revisitar a distribuição visual do painel web e da TUI
- [ ] 3. Qualificar o candidato final e preparar a publicação coordenada com o Batuta

Matriz escolhida: Codex e Claude, macOS e Linux, operação completa no web e na TUI.

Ordem solicitada em 2026-10-03: inicialização, planejamento dos bloqueadores, estabilização funcional e depois revisão visual. A fase 1 tem um plano aprovado, em execução com TDD e versões publicadas atuais; as fases 2 e 3 serão detalhadas a partir das evidências e decisões da fase anterior. O usuário aprovou a execução do plano em 2026-10-03; publicação estável do Console ainda não foi autorizada. A publicação intermediária da API Core/PR145 foi autorizada explicitamente em2026-10-05 e resultou em beta47, sem alterar os gates finais.

Fase 2: avaliar telas e estados reais, hierarquia de informação, navegação, distribuição entre missão/plano/progresso/atenção/evidências/terminal, comportamento em diferentes larguras e acessibilidade. Na TUI, revisar áreas, foco, atalhos e legibilidade em tamanhos de terminal representativos. Comparar alternativas antes de aprovar o desenho e dividi-lo em tarefas de implementação. R27 entra nesta fase: extração de componentes quando necessária, carregamento do terminal e uso sem fontes externas. Preservar os contratos funcionais qualificados na fase 1.

Fase 3: repetir a jornada completa depois da revisão visual, validar instalação e atualização do pacote, conferir documentação/checksums/licenças e fechar a matriz Console/core/plugin. Definir repositório remoto, tags e destino de publicação antes do envio externo. Uma versão estável do core indisponível ou incompatível mantém a publicação pendente; não inventar uma versão para fechar o marco.
