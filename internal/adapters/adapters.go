package adapters

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/batuta-ai/console/internal/domain"
)

type Adapter struct {
	ID         string
	Provider   string
	CLI        string
	Isolation  domain.Isolation
	AuthMethod string
	SessionCap int
	Models     []string
	LimitKnown bool
}

func All() []Adapter {
	return []Adapter{
		{ID: "codex", Provider: "OpenAI", CLI: "codex", Isolation: domain.IsolationSupported, AuthMethod: "official_browser", SessionCap: 3, Models: []string{"gpt-5-codex", "o4-mini"}, LimitKnown: true},
		{ID: "claude", Provider: "Anthropic", CLI: "claude", Isolation: domain.IsolationNotYetQualified, AuthMethod: "cli_login", SessionCap: 1, Models: []string{"sonnet", "opus", "haiku"}},
		{ID: "cursor-agent", Provider: "Cursor", CLI: "cursor-agent", Isolation: domain.IsolationNotYetQualified, AuthMethod: "cli_login", SessionCap: 2, Models: []string{"composer-2.5", "grok-4.6"}},
		{ID: "opencode", Provider: "OpenCode", CLI: "opencode", Isolation: domain.IsolationSupported, AuthMethod: "api_key_vault", SessionCap: 4, Models: []string{"catalog"}},
		{ID: "gemini", Provider: "Google", CLI: "gemini", Isolation: domain.IsolationUnsupported, AuthMethod: "cli_login", SessionCap: 1, Models: []string{"gemini-pro", "gemini-flash"}},
	}
}

func IDs() []string {
	all := All()
	ids := make([]string, 0, len(all))
	for _, a := range all {
		ids = append(ids, a.ID)
	}
	return ids
}

func Get(id string) (Adapter, error) {
	for _, a := range All() {
		if a.ID == id {
			return a, nil
		}
	}
	return Adapter{}, fmt.Errorf("unknown adapter %q", id)
}

func (a Adapter) Lookup() (string, error) {
	return exec.LookPath(a.CLI)
}

// LoggedIn reports whether this account's isolate directory already holds a CLI
// credential file. A connected folder without that file is not a login.
func (a Adapter) LoggedIn(consoleHome string, account domain.Account) bool {
	dir := a.IsolateDir(consoleHome, account.ID)
	if dir == "" {
		return false
	}
	var paths []string
	switch a.ID {
	case "codex":
		paths = []string{filepath.Join(dir, ".codex", "auth.json")}
	case "claude":
		paths = []string{filepath.Join(dir, ".claude", ".credentials.json")}
	default:
		return false
	}
	for _, path := range paths {
		st, err := os.Stat(path)
		if err == nil && !st.IsDir() && st.Size() > 0 {
			return true
		}
	}
	return false
}

func (a Adapter) IsolateDir(consoleHome, accountID string) string {
	if consoleHome == "" || accountID == "" {
		return ""
	}
	return filepath.Join(consoleHome, "accounts", accountID)
}

// IsolateEnv returns HOME (and Codex/Claude config homes) for this account
// so two accounts of the same CLI do not share credentials.
func (a Adapter) IsolateEnv(consoleHome string, account domain.Account) []string {
	dir := a.IsolateDir(consoleHome, account.ID)
	if dir == "" {
		return nil
	}
	_ = os.MkdirAll(dir, 0o700)
	cfg := filepath.Join(dir, ".config")
	_ = os.MkdirAll(cfg, 0o700)
	env := []string{"HOME=" + dir, "XDG_CONFIG_HOME=" + cfg}
	switch a.ID {
	case "codex":
		codexHome := filepath.Join(dir, ".codex")
		_ = os.MkdirAll(codexHome, 0o700)
		env = append(env, "CODEX_HOME="+codexHome)
	case "claude":
		claudeHome := filepath.Join(dir, ".claude")
		_ = os.MkdirAll(claudeHome, 0o700)
		env = append(env, "CLAUDE_CONFIG_DIR="+claudeHome)
	case "cursor-agent":
		cursorHome := filepath.Join(dir, ".cursor")
		_ = os.MkdirAll(cursorHome, 0o700)
		env = append(env, "CURSOR_CONFIG_DIR="+cursorHome)
	}
	return env
}

func NewAccount(label, adapterID string) (domain.Account, error) {
	ad, err := Get(adapterID)
	if err != nil {
		return domain.Account{}, err
	}
	if label == "" {
		return domain.Account{}, fmt.Errorf("account label is required")
	}
	return domain.Account{
		ID:         domain.NewID("acct_"),
		Label:      label,
		Provider:   ad.Provider,
		Adapter:    ad.ID,
		Isolation:  ad.Isolation,
		Connected:  false,
		SessionCap: ad.SessionCap,
		AuthMethod: ad.AuthMethod,
	}, nil
}
