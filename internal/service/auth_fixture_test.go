package service

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
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

// The fixture uses a native TLS provider response and protected synthetic files.
// synthetic_identity remains an independent PTY-child observation, not auth proof.
func authenticateFixtureAccount(t *testing.T, svc *Service, account domain.Account, identity string) {
	authenticateFixtureAccountWithStatus(t, svc, account, identity, http.StatusOK)
}

func authenticateFixtureAccountWithStatus(t *testing.T, svc *Service, account domain.Account, identity string, status int) {
	t.Helper()
	ad, err := adapters.Get(account.Adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.IsolateEnv(svc.Home, account); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if account.Adapter == "codex" {
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": account.ID}})
		token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
		payload = map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": token, "id_token": token, "refresh_token": "synthetic-refresh-never-real", "account_id": account.ID}}
	} else {
		payload = map[string]any{"claudeAiOauth": map[string]any{"accessToken": "console-synthetic-" + account.ID, "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}}
	}
	payload["synthetic_identity"] = identity
	config, name := ".codex", "auth.json"
	if account.Adapter == "claude" {
		config, name = ".claude", ".credentials.json"
	}
	data, _ := json.Marshal(payload)
	if err := testfixture.PreparePrivateFile(filepath.Join(ad.IsolateDir(svc.Home, account.ID), config, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ad.IsolateDir(svc.Home, account.ID), config, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"chatgpt.com", "api.anthropic.com"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
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
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.Method != http.MethodGet {
			t.Error("readiness invoked a non-read-only provider operation")
			w.WriteHeader(405)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		switch r.Host {
		case "chatgpt.com":
			parts := strings.Split(bearer, ".")
			if len(parts) != 3 {
				w.WriteHeader(401)
				return
			}
			claims, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				w.WriteHeader(401)
				return
			}
			var decoded struct {
				Auth struct {
					ID string `json:"chatgpt_account_id"`
				} `json:"https://api.openai.com/auth"`
			}
			if json.Unmarshal(claims, &decoded) != nil || decoded.Auth.ID == "" || r.Header.Get("ChatGPT-Account-Id") != decoded.Auth.ID || r.URL.Path != "/backend-api/wham/usage" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user_id": "synthetic-user", "account_id": decoded.Auth.ID, "plan_type": "pro", "rate_limit": map[string]bool{"allowed": true, "limit_reached": false}})
		case "api.anthropic.com":
			if !strings.HasPrefix(bearer, "console-synthetic-acct_") || r.URL.Path != "/api/oauth/profile" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]string{"uuid": "synthetic-user"}, "organization": map[string]string{"uuid": "synthetic-org"}})
		default:
			t.Error("unexpected provider destination")
			w.WriteHeader(400)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "chatgpt.com:443" && address != "api.anthropic.com:443" {
			t.Errorf("unqualified provider destination %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	t.Cleanup(transport.CloseIdleConnections)
	probe := svc.diagnosticProbe()
	var provider http.RoundTripper = transport
	if t.Name() == "TestConcurrentLoginInitReuse/init" && neverShipInitTraceEnabled() {
		probe = neverShipInitProbe(probe)
		provider = neverShipInitTransport{transport}
	}
	svc.Readiness = adapters.NewReadinessChecker(probe, &http.Client{Transport: provider})
}

func neverShipInitProbe(delegate adapters.DiagnosticProbe) adapters.DiagnosticProbe {
	return func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
		neverShipInitObserve(ctx, "probe_enter", nil, -1)
		output, exit, err := delegate(ctx, argv, env, dir)
		neverShipInitObserve(ctx, "probe_exit", err, exit)
		return output, exit, err
	}
}

type neverShipInitTransport struct{ delegate http.RoundTripper }

func (t neverShipInitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	neverShipInitObserve(r.Context(), "provider_transport_enter", nil, -1)
	response, err := t.delegate.RoundTrip(r)
	neverShipInitObserve(r.Context(), "provider_transport_exit", err, -1)
	return response, err
}
