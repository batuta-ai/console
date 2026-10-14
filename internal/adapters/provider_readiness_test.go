package adapters_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
)

func providerTLSClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"api.anthropic.com", "chatgpt.com"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.anthropic.com:443" && address != "chatgpt.com:443" {
			t.Errorf("unexpected credential destination: %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func authFixture(t *testing.T, id string, client *http.Client) (*adapters.ReadinessChecker, *session.Manager, string, domain.Account, string) {
	t.Helper()
	ad, err := adapters.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "console")
	account := domain.Account{ID: "qa-provider", Adapter: id, Connected: true}
	if _, err := ad.IsolateEnv(home, account); err != nil {
		t.Fatal(err)
	}
	path := testfixture.PrivateHome(t)
	banner := "codex-cli 0.160.0"
	if id == "claude" {
		banner = "2.1.289 (Claude Code)"
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + banner + "'; exit 0; fi\nexit 91\n"
	if err := testfixture.WriteScript(t, filepath.Join(path, testfixture.ExecutableName(id)), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	token := "console-synthetic-provider-token-never-real"
	var payload any
	config, name := ".claude", ".credentials.json"
	if id == "codex" {
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "synthetic-account"}})
		token = "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
		payload = map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": token, "id_token": token, "refresh_token": "synthetic-refresh-never-real", "account_id": "synthetic-account"}}
		config, name = ".codex", "auth.json"
	} else {
		payload = map[string]any{"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": "synthetic-refresh-never-real", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}}
	}
	data, _ := json.Marshal(payload)
	if err := testfixture.PreparePrivateFile(filepath.Join(ad.IsolateDir(home, account.ID), config, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ad.IsolateDir(home, account.ID), config, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	m := session.New(t.TempDir())
	probe := func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
		r, e := m.Probe(ctx, argv, env, dir)
		return r.Output, r.ExitCode, e
	}
	checker := adapters.NewReadinessChecker(probe, client)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return checker, m, home, account, token
}

func TestProviderReadinessRequiresAuthoritativeHTTPSResponse(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				status     int
				body, code string
				ready      bool
			}{
				{"verified", 200, "", "ready", true},
				{"revoked", 401, `{"error":"token-value-must-not-escape"}`, "provider_rejected", false},
				{"forbidden", 403, `{"error":"token-value-must-not-escape"}`, "provider_forbidden", false},
				{"unavailable", 503, `{"error":"token-value-must-not-escape"}`, "provider_unavailable", false},
				{"unqualified-schema", 200, `{"loggedIn":true}`, "provider_response_invalid", false},
				{"oversized-response", 200, strings.Repeat("x", (64<<10)+1), "provider_response_invalid", false},
				{"credential-redirect", 302, "", "provider_redirect", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var requests atomic.Int32
					var expectedToken string
					client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+expectedToken {
							t.Error("wrong provider method or selected credential")
						}
						expectedHost, expectedPath := "api.anthropic.com", "/api/oauth/profile"
						body := `{"account":{"uuid":"synthetic-user"},"organization":{"uuid":"synthetic-org"}}`
						if id == "codex" {
							expectedHost, expectedPath = "chatgpt.com", "/backend-api/wham/usage"
							body = `{"user_id":"synthetic-user","account_id":"synthetic-account","plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false}}`
						} else if r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
							t.Error("missing qualified Claude OAuth header")
						}
						if r.Host != expectedHost || r.URL.Path != expectedPath {
							t.Errorf("unqualified provider target: %s %s", r.Host, r.URL.Path)
						}
						if tc.status == 302 {
							w.Header().Set("Location", "https://unqualified.invalid/credential-sink")
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(tc.status)
						if tc.body != "" {
							body = tc.body
						}
						_, _ = w.Write([]byte(body))
					}))
					checker, _, home, account, token := authFixture(t, id, client)
					expectedToken = token
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					got := checker.Check(ctx, home, account)
					if got.Ready != tc.ready || got.Authenticated != tc.ready || !got.CredentialConfigured || !got.Available || !got.CLICompatible || got.Code != tc.code {
						t.Fatalf("wrong verified readiness: %+v", got)
					}
					if requests.Load() != 1 {
						t.Fatalf("provider request count=%d; redirects must not forward credentials", requests.Load())
					}
				})
			}
		})
	}
}

func TestExpiredAndMalformedCredentialsNeverReachProvider(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		for _, state := range []string{"expired", "malformed"} {
			t.Run(id+"/"+state, func(t *testing.T) {
				var requests atomic.Int32
				client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
				checker, _, home, account, _ := authFixture(t, id, client)
				config, name := ".claude", ".credentials.json"
				if id == "codex" {
					config, name = ".codex", "auth.json"
				}
				path := filepath.Join(home, "accounts", account.ID, config, name)
				data := []byte(`{"loggedIn":true}`)
				want := "credential_invalid"
				if state == "expired" {
					want = "credential_expired"
					if id == "codex" {
						token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1}`)) + ".synthetic"
						data, _ = json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": token, "account_id": "synthetic-account"}})
					} else {
						data = []byte(`{"claudeAiOauth":{"accessToken":"synthetic-expired","expiresAt":1,"scopes":["user:inference","user:profile"]}}`)
					}
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				got := checker.Check(context.Background(), home, account)
				if got.Ready || got.Authenticated || !got.CredentialConfigured || !got.CLICompatible || got.Code != want || requests.Load() != 0 {
					t.Fatalf("invalid credential used: %+v requests=%d", got, requests.Load())
				}
			})
		}
	}
}

func TestReadinessRequiresVerifiedTLS(t *testing.T) {
	var requests atomic.Int32
	client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = x509.NewCertPool()
	checker, _, home, account, _ := authFixture(t, "claude", client)
	got := checker.Check(context.Background(), home, account)
	if got.Ready || got.Authenticated || got.Code != "provider_unavailable" || requests.Load() != 0 {
		t.Fatalf("untrusted TLS accepted: %+v requests=%d", got, requests.Load())
	}
}

func TestCodexNullableLimitsDoNotInvalidateAuthentication(t *testing.T) {
	for _, body := range []string{`{"user_id":"synthetic-user","account_id":"synthetic-account","plan_type":"plus","rate_limit":null}`, `{"user_id":"synthetic-user","account_id":"synthetic-account","plan_type":"plus"}`, `{"user_id":null,"account_id":null,"plan_type":"plus","rate_limit":null}`, `{"plan_type":"plus"}`} {
		client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		checker, _, home, account, _ := authFixture(t, "codex", client)
		got := checker.Check(context.Background(), home, account)
		if !got.Authenticated || !got.Ready || got.Code != "ready" {
			t.Fatalf("nullable usage limits invalidated verified identity: %+v", got)
		}
	}
}

func TestCodexResponseMustMatchSelectedAccountWhenPresent(t *testing.T) {
	client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user_id":"synthetic-user","account_id":"other-account","plan_type":"plus","rate_limit":null}`))
	}))
	checker, _, home, account, _ := authFixture(t, "codex", client)
	got := checker.Check(context.Background(), home, account)
	if got.Ready || got.Authenticated || got.Code != "provider_response_invalid" {
		t.Fatalf("mismatched account authenticated: %+v", got)
	}
}

func TestUnsupportedCodexAuthenticationMethodIsDistinct(t *testing.T) {
	var requests atomic.Int32
	client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	checker, _, home, account, _ := authFixture(t, "codex", client)
	path := filepath.Join(home, "accounts", account.ID, ".codex", "auth.json")
	if err := os.WriteFile(path, []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-never-real"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := checker.Check(context.Background(), home, account)
	if got.Ready || got.Authenticated || got.Code != "auth_method_unsupported" || requests.Load() != 0 {
		t.Fatalf("unsupported authentication misclassified: %+v requests=%d", got, requests.Load())
	}
}

func TestCredentialFilesMustBePrivateAndNotLinked(t *testing.T) {
	for _, mode := range []string{"open-permissions", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(401) }))
			checker, _, home, account, _ := authFixture(t, "claude", client)
			path := filepath.Join(home, "accounts", account.ID, ".claude", ".credentials.json")
			switch mode {
			case "open-permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "other-profile")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(t.TempDir(), "other-profile")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got := checker.Check(ctx, home, account)
			if got.Ready || got.Authenticated || got.Code != "credential_invalid" || requests.Load() != 0 {
				t.Fatalf("unsafe credential reached provider or authenticated: %+v requests=%d", got, requests.Load())
			}
		})
	}
}

func TestReadinessHasOneOverallDeadline(t *testing.T) {
	var requests atomic.Int32
	client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-time.After(3 * time.Second):
			_, _ = w.Write([]byte(`{"account":{"uuid":"synthetic-user"},"organization":{"uuid":"synthetic-org"}}`))
		case <-r.Context().Done():
		}
	}))
	checker, _, home, account, _ := authFixture(t, "claude", client)
	script := "#!/bin/sh\n/bin/sleep 3\nprintf '%s\\n' '2.1.289 (Claude Code)'\n"
	if err := testfixture.RewriteScript(t, filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName("claude")), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := checker.Check(context.Background(), home, account)
	if got.Ready || got.Authenticated || got.Code != "check_cancelled" || time.Since(start) > 6*time.Second || requests.Load() != 1 {
		t.Fatalf("CLI and HTTP did not share the five-second budget: %+v elapsed=%s requests=%d", got, time.Since(start), requests.Load())
	}
}

func TestReadinessPreservesConsoleHomeAndRejectsUnsafePrivateHome(t *testing.T) {
	for _, mode := range []string{"console-readable", "private-writable"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			client := providerTLSClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`{"account":{"uuid":"synthetic-user"},"organization":{"uuid":"synthetic-org"}}`))
			}))
			checker, _, home, account, _ := authFixture(t, "claude", client)
			path, perm := home, os.FileMode(0755)
			if mode == "private-writable" {
				path, perm = filepath.Join(home, "accounts", account.ID), 0777
			}
			if err := os.Chmod(path, perm); err != nil {
				t.Fatal(err)
			}
			got := checker.Check(context.Background(), home, account)
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != perm {
				t.Fatalf("readiness changed existing permissions: %v %v", info, err)
			}
			if mode == "console-readable" {
				if !got.Ready || requests.Load() != 1 {
					t.Fatalf("safe Console root rejected: %+v requests=%d", got, requests.Load())
				}
			} else if got.Ready || got.CLICompatible || got.Code != "isolation_invalid" || requests.Load() != 0 {
				t.Fatalf("unsafe private root used: %+v requests=%d", got, requests.Load())
			}
		})
	}
}
