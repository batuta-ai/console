package api

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
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/testfixture"
)

// The fixture uses a native TLS provider response and protected synthetic files.
// synthetic_identity remains an independent PTY-child observation, not auth proof.
func authenticateInitHTTPAccount(t *testing.T, svc *service.Service, account domain.Account, identity string) {
	authenticateInitHTTPAccountWithStatus(t, svc, account, identity, http.StatusOK)
}

func authenticateInitHTTPAccountWithStatus(t *testing.T, svc *service.Service, account domain.Account, identity string, status int) {
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
	svc.Readiness = adapters.NewReadinessChecker(func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
		r, err := svc.Session.Probe(ctx, argv, env, dir)
		return r.Output, r.ExitCode, err
	}, &http.Client{Transport: transport})
}

func TestExplicitInitNativeHTTP(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			h, svc := testServer(t)
			cliDir := testfixture.PrivateHome(t)
			banner := "codex-cli 0.160.0"
			if adapter == "claude" {
				banner = "2.1.289 (Claude Code)"
			}
			cli := filepath.Join(cliDir, testfixture.ExecutableName(adapter))
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + banner + "'; exit 0; fi\n[ -t 0 ] && [ -t 1 ] || exit 91\nwhile IFS= read -r line; do printf 'ack:%s\\n' \"$line\"; done\n"
			if err := testfixture.WriteScript(t, cli, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", cliDir)
			skills := t.TempDir()
			for _, name := range []string{"batuta", "batuta-init"} {
				if err := os.MkdirAll(filepath.Join(skills, name, "adapters"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(skills, name, "SKILL.md"), []byte("# "+name+"\nSynthetic native fixture."), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BATUTA_SKILLS", filepath.Join(skills, "batuta"))
			a, err := svc.CreateAccount("selected", adapter)
			if err != nil {
				t.Fatal(err)
			}
			a, err = svc.ConnectAccount(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			authenticateInitHTTPAccount(t, svc, a, "A")
			w, err := svc.CreateWorkspace("synthetic", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan domain.Session, 1)
			onExit := svc.Session.OnExit
			svc.Session.OnExit = func(row domain.Session) { onExit(row); done <- row }
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := svc.Session.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			body := setupBody(a.ID)
			body["owner"] = "web"
			body["client"] = "test"
			path := "/v0/workspaces/" + w.ID + "/init"
			result := post(t, h, path, body, 200)
			id, ok := result["id"].(string)
			if !ok || result["account_id"] != a.ID || result["adapter"] != adapter || result["work"] != "init" {
				t.Fatalf("init response=%v", result)
			}
			argv, ok := result["argv"].([]any)
			want := "Run batuta-init for this project. Write .batuta/profile.md, .batuta/routing.md and WORK.md from these confirmed answers, preserving existing user content, then stop.\n\nStack: Go\nMethodology: TDD\nTest: go test ./...\nBuild: go build ./...\nInstall: go mod download\nExecution: sequential\nWorktree: medium+\nTemplate: templates/go.md\n\nAdditional notes:\nliteral notes; $(not-executed)"
			if !ok || len(argv) != 2 || argv[0] != cli || argv[1] != want {
				t.Fatalf("literal argv=%v", argv)
			}
			reused := post(t, h, path, body, 200)
			if reused["id"] != id {
				t.Fatalf("reuse=%v", reused)
			}
			body["brief"] = "changed notes"
			post(t, h, path, body, 409)
			rows, err := svc.Store.ListSessions()
			if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].AccountID != a.ID {
				t.Fatalf("catalog=%v %v", rows, err)
			}
			before := get(t, h, "/v0/workspaces").([]any)[0].(map[string]any)["setup"].(map[string]any)
			if before["state"] != "running" || before["ready"] != false {
				t.Fatalf("running DTO=%v", before)
			}
			post(t, h, "/v0/sessions/"+id+"/stop", map[string]any{}, 200)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("exit callback not committed")
			}
			after := get(t, h, "/v0/workspaces").([]any)[0].(map[string]any)["setup"].(map[string]any)
			attempt := after["attempt"].(map[string]any)
			if after["state"] != "cancelled" || after["ready"] != false || attempt["session_id"] != id || attempt["account_id"] != a.ID || attempt["stop_requested"] != true || attempt["state"] != "cancel_requested" {
				t.Fatalf("stopped DTO=%v", after)
			}
		})
	}
}
