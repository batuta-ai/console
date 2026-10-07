package i18n

import (
	"os"
	"strings"
)

func Lang() string {
	if v := os.Getenv("BATUTA_LANG"); v != "" {
		if strings.HasPrefix(strings.ToLower(v), "pt") {
			return "pt"
		}
		return "en"
	}
	for _, k := range []string{"LC_ALL", "LANG"} {
		if strings.HasPrefix(strings.ToLower(os.Getenv(k)), "pt") {
			return "pt"
		}
	}
	return "en"
}

func T(key string) string {
	pack := en
	if Lang() == "pt" {
		pack = pt
	}
	if s, ok := pack[key]; ok {
		return s
	}
	return en[key]
}

var en = map[string]string{
	"account_pending_list_keys":   "Request pending: h home  q quit",
	"account_recovery_list_keys":  "f inspect  d acknowledge after inspection  h home  q quit",
	"account_pending_keys":        "Request pending: esc cancel/back  Ctrl+C quit",
	"account_recovery_keys":       "f inspect  d acknowledge after inspection  esc back  Ctrl+C quit",
	"account_sessions":            "Actual sessions: ID / account / provider / work / process",
	"account_session_keys":        "[ / ] select actual session  v view (no take); unknown processes cannot be reattached",
	"account_empty":               "No accounts yet. n new account (Codex / Claude); c accounts.",
	"account_new":                 "New isolated account",
	"account_label":               "Label",
	"account_label_required":      "Enter a label before creating the account.",
	"account_form_keys":           "Type label  tab provider  enter create  esc cancel/back  Ctrl+C quit",
	"account_detail_keys":         "k connect home  l official login  f refresh readiness  n new account  esc back  q quit",
	"account_list_keys":           "↑↓ select  enter details  n new account  f inspect/refresh  h home  q quit",
	"account_columns":             "ACCOUNT              ISOLATION           LIMIT                    BAR",
	"account_connection_help":     "Connect prepares the private HOME. Official login opens a terminal session; it does not prove authentication.",
	"account_isolation":           "Isolation",
	"account_connected":           "Home connected",
	"account_authenticated":       "Authenticated (last check)",
	"account_yes":                 "yes",
	"account_no":                  "no",
	"account_unknown":             "Readiness unknown. Refresh before login.",
	"account_unsupported":         "Unsupported account or platform. Check provider/platform support.",
	"account_cli_unavailable":     "CLI unavailable. Install the official provider CLI, then refresh.",
	"account_cli_incompatible":    "CLI incompatible. Check the supported official CLI version, then refresh.",
	"account_probe_unavailable":   "CLI check unavailable. Inspect the daemon diagnostics, then refresh readiness.",
	"account_cli_probe_failed":    "CLI version check failed. Check that the official CLI can report its version, then refresh readiness.",
	"account_check_cancelled":     "Readiness check cancelled. Refresh to run a new check before login.",
	"account_isolation_invalid":   "Private home isolation is invalid. Inspect the account's private directories before refreshing readiness.",
	"account_connect_required":    "Connect the private home with k before login.",
	"account_auth_pending":        "Authentication pending. Use official login, then refresh readiness.",
	"account_not_ready":           "Authenticated, but not ready. Refresh and inspect the account.",
	"account_ready":               "Account ready at the last check.",
	"account_missing":             "Account absent from the latest catalog. Last confirmed identity retained; inspect with f.",
	"account_detach_first":        "Detach the current terminal with Ctrl-] before opening login.",
	"account_pty_unavailable":     "Runtime PTY unavailable or unsupported. Inspect platform support and refresh.",
	"account_read_failed":         "Read failed. Last confirmed catalog and draft retained; f retries inspection.",
	"account_retry":               "Request not dispatched or rejected before mutation. Retry the explicit action when ready.",
	"account_uncertain":           "Possible effect: do not repeat the mutation. Inspect accounts and sessions with f.",
	"account_invalid_reply":       "Unconfirmed reply. Inspect actual accounts and sessions before another action.",
	"account_completed_elsewhere": "Request completed after navigation. Inspect the catalog and select the actual account/session.",
	"account_login_started":       "Login session opened; authentication is unconfirmed. Return to accounts and refresh after completing official login.",
	"account_pending":             "Request pending. Esc cancels and joins; no repeated dispatch.",
	"account_possible_effect":     "Pending inspection",
	"account_inspect":             "f inspect without mutation. Use actual account IDs and explicit session selection; never match by label.",
	"account_acknowledge":         "After inspecting: d acknowledge and return to exact account selection. This does not undo a possible effect.",
	"account_action_create":       "create",
	"account_action_connect":      "connect",
	"account_action_login":        "login session",

	"title": "BATUTA", "home": "Home", "missions": "Missions", "squads": "Squads",
	"accounts": "Accounts", "activity": "Activity", "room": "Room", "review": "Review",
	"needs_you": "Needs you", "unknown": "unknown", "save": "Save does not run a model.",
	"keys":   "↑↓ navigate  enter open  c accounts  s squads  r review  q quit",
	"term":   "TERMINAL MODE · keys go to child · Ctrl-] detaches to navigation",
	"daemon": "consoled",
}

var pt = map[string]string{
	"account_pending_list_keys":   "Solicitação pendente: h início  q sair",
	"account_recovery_list_keys":  "f inspecionar  d confirmar após inspeção  h início  q sair",
	"account_pending_keys":        "Solicitação pendente: esc cancelar/voltar  Ctrl+C sair",
	"account_recovery_keys":       "f inspecionar  d confirmar após inspeção  esc voltar  Ctrl+C sair",
	"account_sessions":            "Sessões reais: ID / conta / provedor / trabalho / processo",
	"account_session_keys":        "[ / ] selecionar sessão real  v visualizar (sem assumir controle); processos desconhecidos não podem ser reconectados",
	"account_empty":               "Nenhuma conta. n nova conta (Codex / Claude); c contas.",
	"account_new":                 "Nova conta isolada",
	"account_label":               "Nome",
	"account_label_required":      "Informe um nome antes de criar a conta.",
	"account_form_keys":           "Digite o nome  tab provedor  enter criar  esc cancelar/voltar  Ctrl+C sair",
	"account_detail_keys":         "k conectar home  l login oficial  f atualizar prontidão  n nova conta  esc voltar  q sair",
	"account_list_keys":           "↑↓ selecionar  enter detalhes  n nova conta  f inspecionar/atualizar  h início  q sair",
	"account_columns":             "CONTA                ISOLAMENTO          LIMITE                   BARRA",
	"account_connection_help":     "Conectar prepara o HOME privado. O login oficial abre uma sessão de terminal; isso não comprova autenticação.",
	"account_isolation":           "Isolamento",
	"account_connected":           "Home conectado",
	"account_authenticated":       "Autenticada (última verificação)",
	"account_yes":                 "sim",
	"account_no":                  "não",
	"account_unknown":             "Prontidão desconhecida. Atualize antes do login.",
	"account_unsupported":         "Não suportado: conta ou plataforma. Confira o suporte ao provedor/plataforma.",
	"account_cli_unavailable":     "CLI indisponível. Instale o CLI oficial do provedor e atualize.",
	"account_cli_incompatible":    "CLI incompatível. Confira a versão suportada do CLI oficial e atualize.",
	"account_probe_unavailable":   "Verificação do CLI indisponível. Inspecione os diagnósticos do daemon e atualize a prontidão.",
	"account_cli_probe_failed":    "Falha ao verificar a versão do CLI. Confira se o CLI oficial consegue informar sua versão e atualize a prontidão.",
	"account_check_cancelled":     "Verificação de prontidão cancelada. Atualize para executar uma nova verificação antes do login.",
	"account_isolation_invalid":   "Isolamento do home privado inválido. Inspecione os diretórios privados da conta antes de atualizar a prontidão.",
	"account_connect_required":    "Conecte o home privado com k antes do login.",
	"account_auth_pending":        "Autenticação pendente. Use o login oficial e atualize a prontidão.",
	"account_not_ready":           "Autenticada, mas ainda não está pronta. Atualize e inspecione a conta.",
	"account_ready":               "Conta pronta na última verificação.",
	"account_missing":             "Conta ausente no catálogo atual. Identidade confirmada preservada; inspecione com f.",
	"account_detach_first":        "Desconecte o terminal atual com Ctrl-] antes de abrir o login.",
	"account_pty_unavailable":     "PTY indisponível ou não suportado nesta execução. Confira o suporte à plataforma e atualize.",
	"account_read_failed":         "Falha na leitura. Catálogo confirmado e rascunho preservados; f repete a inspeção.",
	"account_retry":               "Solicitação não enviada ou rejeitada antes da alteração. Repita a ação explicitamente quando estiver pronto.",
	"account_uncertain":           "Efeito possível: não repita a alteração. Inspecione contas e sessões com f.",
	"account_invalid_reply":       "Resposta não confirmada. Inspecione as contas e sessões reais antes de outra ação.",
	"account_completed_elsewhere": "Solicitação concluída após a navegação. Inspecione o catálogo e selecione a conta/sessão real.",
	"account_login_started":       "Sessão de login aberta; autenticação não confirmada. Volte às contas e atualize após concluir o login oficial.",
	"account_pending":             "Solicitação pendente. Esc cancela e aguarda a conclusão; nenhum envio repetido.",
	"account_possible_effect":     "Inspeção pendente",
	"account_inspect":             "f inspecionar sem alterar. Use IDs reais de conta e seleção explícita de sessão; nunca associe pelo nome.",
	"account_acknowledge":         "Após inspecionar: d confirmar e voltar à seleção exata da conta. Isso não desfaz um possível efeito.",
	"account_action_create":       "criar",
	"account_action_connect":      "conectar",
	"account_action_login":        "sessão de login",

	"title": "BATUTA", "home": "Início", "missions": "Missões", "squads": "Squads",
	"accounts": "Contas", "activity": "Atividade", "room": "Sala", "review": "Revisão",
	"needs_you": "Precisa de você", "unknown": "desconhecido", "save": "Salvar não executa modelo.",
	"keys":   "↑↓ navegar  enter abrir  c contas  s squads  r revisão  q sair",
	"term":   "MODO TERMINAL · teclas vão ao processo · Ctrl-] desconecta para navegação",
	"daemon": "consoled",
}
