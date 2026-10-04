package adapters

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

// DiagnosticProbe is supplied by the service's owned Manager, without coupling
// the adapter catalog to the session/accounts packages.
type DiagnosticProbe func(context.Context, []string, []string, string) ([]byte, int, error)

type ReadinessChecker struct {
	probe  DiagnosticProbe
	client *http.Client
}

func NewReadinessChecker(probe DiagnosticProbe, client *http.Client) *ReadinessChecker {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	closed := *client
	closed.Timeout = 5 * time.Second
	closed.Jar = nil
	closed.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ReadinessChecker{probe: probe, client: &closed}
}

func (c *ReadinessChecker) Check(ctx context.Context, home string, account domain.Account) domain.AccountReadiness {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r := c.CheckCLI(ctx, home, account)
	if !r.CLICompatible {
		return r
	}
	return c.authenticate(ctx, home, account, r)
}

// CheckCLI allows login to repair invalid or expired credentials without
// authorizing model execution from an offline credential-presence check.
func (c *ReadinessChecker) CheckCLI(ctx context.Context, home string, account domain.Account) domain.AccountReadiness {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r := domain.AccountReadiness{Connected: account.Connected, CheckedAt: time.Now().UTC()}
	ad, err := Get(account.Adapter)
	if err != nil || (ad.ID != "codex" && ad.ID != "claude") {
		r.Code = "adapter_unsupported"
		return r
	}
	if !SelectedPlatform(runtime.GOOS) {
		r.Code = "platform_unsupported"
		return r
	}
	r.Supported = true
	path, err := ad.Lookup()
	if err != nil {
		r.Code = "cli_missing"
		return r
	}
	r.Available = true
	if !account.Connected {
		r.Code = "account_disconnected"
		return r
	}
	if !validIsolate(home, account.ID) || !privateIdentityDirectories(home, account) {
		r.Code = "isolation_invalid"
		return r
	}
	env, err := ad.IsolateEnv(home, account)
	if err != nil {
		r.Code = "isolation_invalid"
		return r
	}
	if c.probe == nil {
		r.Code = "probe_unavailable"
		return r
	}
	version, exit, err := c.probe(ctx, []string{path, "--version"}, env, ad.IsolateDir(home, account.ID))
	if err != nil || exit != 0 {
		r.Code = cancelCode(ctx, "cli_probe_failed")
		return r
	}
	banner := strings.TrimSpace(string(version))
	if ctx.Err() != nil {
		r.Code = "check_cancelled"
		return r
	}
	qualified := compatibleBanner(ad.ID, banner)
	if !qualified {
		r.Code = "cli_incompatible"
		return r
	}
	r.CLICompatible = true
	r.Code = "authentication_required"
	return r
}

func cancelCode(ctx context.Context, code string) string {
	if ctx.Err() != nil {
		return "check_cancelled"
	}
	return code
}

type credential struct {
	token     string
	accountID string
}

func privateIdentityDirectories(home string, account domain.Account) bool {
	config := ".codex"
	if account.Adapter == "claude" {
		config = ".claude"
	}
	base := filepath.Join(home, "accounts")
	privateHome := filepath.Join(base, account.ID)
	for i, path := range []string{home, base, privateHome, filepath.Join(privateHome, config)} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !credentialOwner(info) {
			return false
		}
		if i == 0 {
			if info.Mode().Perm()&0022 != 0 {
				return false
			}
		} else if info.Mode().Perm() != 0700 {
			return false
		}
	}
	return true
}

func readCredential(home string, account domain.Account) (credential, bool, string) {
	if !validIsolate(home, account.ID) || !privateIdentityDirectories(home, account) {
		return credential{}, false, "isolation_invalid"
	}
	config, name := ".codex", "auth.json"
	if account.Adapter == "claude" {
		config, name = ".claude", ".credentials.json"
	}
	privateHome := filepath.Join(home, "accounts", account.ID)
	data, err := readPrivateCredential(filepath.Join(privateHome, config, name))
	if errors.Is(err, os.ErrNotExist) {
		return credential{}, false, "credential_missing"
	}
	if err != nil {
		return credential{}, true, "credential_invalid"
	}
	if account.Adapter == "codex" {
		var d struct {
			AuthMode string `json:"auth_mode"`
			Tokens   struct {
				Access    string `json:"access_token"`
				AccountID string `json:"account_id"`
			} `json:"tokens"`
		}
		if json.Unmarshal(data, &d) != nil || d.AuthMode == "" {
			return credential{}, true, "credential_invalid"
		}
		if d.AuthMode != "chatgpt" {
			return credential{}, true, "auth_method_unsupported"
		}
		parts := strings.Split(d.Tokens.Access, ".")
		if len(parts) != 3 {
			return credential{}, true, "credential_invalid"
		}
		claims, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return credential{}, true, "credential_invalid"
		}
		var parsed struct {
			Exp  int64 `json:"exp"`
			Auth struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(claims, &parsed) != nil || parsed.Exp <= 0 {
			return credential{}, true, "credential_invalid"
		}
		if parsed.Exp <= time.Now().Unix() {
			return credential{}, true, "credential_expired"
		}
		id := d.Tokens.AccountID
		if id == "" {
			id = parsed.Auth.AccountID
		}
		if id == "" || !safeBearer(d.Tokens.Access) || !safeBearer(id) {
			return credential{}, true, "credential_invalid"
		}
		return credential{d.Tokens.Access, id}, true, ""
	}
	var d struct {
		OAuth struct {
			Access  string   `json:"accessToken"`
			Expires int64    `json:"expiresAt"`
			Scopes  []string `json:"scopes"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &d) != nil || !safeBearer(d.OAuth.Access) || d.OAuth.Expires <= 0 {
		return credential{}, true, "credential_invalid"
	}
	if d.OAuth.Expires <= time.Now().UnixMilli() {
		return credential{}, true, "credential_expired"
	}
	inference, profile := false, false
	for _, scope := range d.OAuth.Scopes {
		inference = inference || scope == "user:inference"
		profile = profile || scope == "user:profile"
	}
	if !inference || !profile {
		return credential{}, true, "auth_method_unsupported"
	}
	return credential{token: d.OAuth.Access}, true, ""
}

func safeBearer(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r <= 32 || r >= 127 {
			return false
		}
	}
	return true
}

func (c *ReadinessChecker) authenticate(ctx context.Context, home string, account domain.Account, r domain.AccountReadiness) domain.AccountReadiness {
	cred, configured, code := readCredential(home, account)
	r.CredentialConfigured = configured
	if code != "" {
		r.Code = code
		return r
	}
	r.CredentialSource = "private_file"
	r.Code = c.verify(ctx, account.Adapter, cred)
	r.Authenticated = r.Code == "ready"
	r.Ready = r.Authenticated && r.CLICompatible && r.Available && r.Supported && r.Connected
	return r
}

func (c *ReadinessChecker) verify(ctx context.Context, adapter string, cred credential) string {
	url := "https://api.anthropic.com/api/oauth/profile"
	if adapter == "codex" {
		url = "https://chatgpt.com/backend-api/wham/usage"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "provider_unavailable"
	}
	req.Header.Set("Authorization", "Bearer "+cred.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "batuta-console-auth-check/0.1")
	if adapter == "claude" {
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("ChatGPT-Account-Id", cred.accountID)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return cancelCode(ctx, "provider_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return "provider_redirect"
	}
	if response.StatusCode == 401 {
		return "provider_rejected"
	}
	if response.StatusCode == 403 {
		return "provider_forbidden"
	}
	if response.StatusCode != 200 {
		return "provider_unavailable"
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		return cancelCode(ctx, "provider_unavailable")
	}
	if len(data) > 64<<10 {
		return "provider_response_invalid"
	}
	if adapter == "codex" {
		var d struct {
			AccountID string `json:"account_id"`
			Plan      string `json:"plan_type"`
		}
		if json.Unmarshal(data, &d) != nil || d.Plan == "" || (d.AccountID != "" && d.AccountID != cred.accountID) {
			return "provider_response_invalid"
		}
	} else {
		var d struct {
			Account struct {
				UUID string `json:"uuid"`
			} `json:"account"`
			Organization struct {
				UUID string `json:"uuid"`
			} `json:"organization"`
		}
		if json.Unmarshal(data, &d) != nil || d.Account.UUID == "" || d.Organization.UUID == "" {
			return "provider_response_invalid"
		}
	}
	return cancelCode(ctx, "ready")
}
