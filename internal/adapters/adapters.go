package adapters

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

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

// LoggedIn verifies an isolated file credential with its provider. CLI support
// and execution readiness are separate checks, performed by ReadinessChecker.
func (a Adapter) LoggedIn(consoleHome string, account domain.Account) bool {
	if !account.Connected || (a.ID != "codex" && a.ID != "claude") {
		return false
	}
	if account.Adapter != "" && account.Adapter != a.ID {
		return false
	}
	account.Adapter = a.ID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := NewReadinessChecker(nil, nil)
	defer c.client.CloseIdleConnections()
	return c.authenticate(ctx, consoleHome, account, domain.AccountReadiness{Connected: true}).Authenticated
}

func (a Adapter) IsolateDir(consoleHome, accountID string) string {
	if !validIsolate(consoleHome, accountID) {
		return ""
	}
	return filepath.Join(consoleHome, "accounts", accountID)
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
