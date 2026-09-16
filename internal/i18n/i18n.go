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
	"title": "BATUTA", "home": "Home", "missions": "Missions", "squads": "Squads",
	"accounts": "Accounts", "activity": "Activity", "room": "Room", "review": "Review",
	"needs_you": "Needs you", "unknown": "unknown", "save": "Save does not run a model.",
	"keys": "↑↓ navigate  enter open  t terminal  c accounts  s squads  r review  q quit",
	"term": "TERMINAL MODE · Esc or Ctrl-] returns to navigation",
	"daemon": "consoled",
}

var pt = map[string]string{
	"title": "BATUTA", "home": "Início", "missions": "Missões", "squads": "Squads",
	"accounts": "Contas", "activity": "Atividade", "room": "Sala", "review": "Revisão",
	"needs_you": "Precisa de você", "unknown": "desconhecido", "save": "Salvar não executa modelo.",
	"keys": "↑↓ navegar  enter abrir  t terminal  c contas  s squads  r revisão  q sair",
	"term": "MODO TERMINAL · Esc ou Ctrl-] volta à navegação",
	"daemon": "consoled",
}
