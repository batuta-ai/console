package tui

import (
	"bytes"
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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

// The same protected synthetic credential/TLS profile used by service fixtures,
// carried over net.Pipe. The real readiness checker and owned CLI probe run;
// no provider host is contacted and no readiness result is substituted.
func journeyFixtureAuthentication(t *testing.T, svc *service.Service, a domain.Account) {
	t.Helper()
	ad, err := adapters.Get(a.Adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ad.IsolateEnv(svc.Home, a); err != nil {
		t.Fatal(err)
	}
	var payload any
	dir, name := ".codex", "auth.json"
	if a.Adapter == "codex" {
		claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": a.ID}})
		token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
		payload = map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": token, "id_token": token, "refresh_token": "synthetic-never-real", "account_id": a.ID}}
	} else {
		dir, name = ".claude", ".credentials.json"
		payload = map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-never-real", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}}
	}
	raw, _ := json.Marshal(payload)
	if err = os.WriteFile(filepath.Join(ad.IsolateDir(svc.Home, a.ID), dir, name), raw, 0600); err != nil {
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
	listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("non-read-only auth fixture request")
			http.Error(w, "bad request", 400)
			return
		}
		switch r.Host {
		case "chatgpt.com":
			if r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("ChatGPT-Account-Id") != a.ID {
				t.Error("wrong scoped account", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user_id": "synthetic-user", "account_id": a.ID, "plan_type": "pro", "rate_limit": map[string]bool{"allowed": true, "limit_reached": false}})
		case "api.anthropic.com":
			if r.URL.Path != "/api/oauth/profile" {
				t.Error("wrong auth endpoint")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]string{"uuid": "synthetic-user"}, "organization": map[string]string{"uuid": "synthetic-org"}})
		default:
			t.Error("external destination", r.Host)
			http.Error(w, "bad destination", 400)
		}
	})}
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(tlsListener) }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, DialContext: listener.dial}
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close(); transport.CloseIdleConnections(); <-done })
	svc.Readiness = adapters.NewReadinessChecker(func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
		result, err := svc.Session.Probe(ctx, argv, env, dir)
		return result.Output, result.ExitCode, err
	}, &http.Client{Transport: transport})
}

func TestJourneyRealDaemonPlanningBindingApprovalTwoMissions(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			st, err := store.Open(filepath.Join(root, "catalog.db"))
			if err != nil {
				t.Fatal(err)
			}
			manager := session.New(t.TempDir())
			svc := service.New(st, manager, nil)
			svc.Home = root
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := manager.Shutdown(ctx); err != nil {
					t.Error(err)
				}
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			bin := filepath.Join(root, "bin")
			if err = os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			version := map[string]string{"codex": "codex-cli 0.160.0", "claude": "2.1.289 (Claude Code)"}[provider]
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + version + "'; exit 0; fi\nif [ -n \"$OPENAI_API_KEY\" ] || [ -n \"$ANTHROPIC_API_KEY\" ]; then exit 90; fi\nprintf JOURNEY_READY\nwhile IFS= read -r line; do printf 'ack:%s\\n' \"$line\"; done\n"
			if err = os.WriteFile(filepath.Join(bin, provider), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			skills := filepath.Join(root, "skills")
			for _, name := range []string{"batuta", "batuta-init", "batuta-plan"} {
				p := filepath.Join(skills, name)
				if err = os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(p, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: disposable fixture\n---\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.Mkdir(filepath.Join(skills, "batuta", "adapters"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BATUTA_SKILLS", skills)
			a, err := svc.CreateAccount("fixture", provider)
			if err != nil {
				t.Fatal(err)
			}
			a, err = svc.ConnectAccount(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			journeyFixtureAuthentication(t, svc, a)
			if err = os.Mkdir(filepath.Join(root, "workspace"), 0700); err != nil {
				t.Fatal(err)
			}
			ws, err := svc.CreateWorkspace("exact", filepath.Join(root, "workspace"))
			if err != nil {
				t.Fatal(err)
			}
			// Workspace registration preserves a chosen path; the fixture creates it.
			if err = os.MkdirAll(filepath.Join(ws.Root, ".batuta/plans"), 0700); err != nil {
				t.Fatal(err)
			}
			source := []byte("# Plan — Exact\r\n**Status:** proposed\r\n**Goal:** exact source\r\n\r\n## Tasks\r\n- [ ] 1. Exact task — backend/low\r\n  Scope: out.txt\r\n  Accept: inspect every byte\r\n\r\n## Context\r\nbyte: \xff\r\nEND-SOURCE\r\n")
			path := ".batuta/plans/exact.md"
			if err = os.WriteFile(filepath.Join(ws.Root, path), source, 0600); err != nil {
				t.Fatal(err)
			}
			squad, err := svc.SaveSquad(domain.Squad{Name: "actual squad", Members: []domain.Member{{Role: "lead", CLI: provider, AccountID: a.ID, Maestro: true}}})
			if err != nil {
				t.Fatal(err)
			}
			first, err := svc.CreateMission(ws.ID, "First real mission", "", squad.ID)
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.CreateMission(ws.ID, "Second real mission", "", squad.ID)
			if err != nil {
				t.Fatal(err)
			}
			handler := (&api.Server{Svc: svc, Token: "disposable-account-fixture"}).Handler()
			posts := 0
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts++
				}
				handler.ServeHTTP(w, r)
			})
			m := model{api: c, clientID: "actual-tui", missions: []domain.Mission{first, second}, screen: scrMissions, cursor: 1}
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "p")
			m = journeyKey(t, m, "enter")
			r := m.journeyRecord()
			if r.session == nil || r.session.MissionID != second.ID || r.session.MemberID != second.SquadSnapshot.Members[0].ID || r.session.AccountID != a.ID || !r.session.BatutaInjected || posts != 1 {
				t.Fatal("real planning failed", m.View().Content)
			}
			actual, ok := manager.Snapshot(r.session.ID)
			if !ok || actual.OwnerClient != "actual-tui" || actual.Owner != "tui" {
				t.Fatal("actual manager identity", actual)
			}
			stored, err := st.GetSession(actual.ID)
			if err != nil || stored.MissionID != second.ID {
				t.Fatal("session not persisted", stored, err)
			}
			m = journeyKey(t, m, "b")
			m = catalogPaste(m, path)
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "tab")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "enter")
			m = journeyKey(t, m, "o")
			m = journeyInspectAll(t, m)
			m = journeyKey(t, m, "y")
			m = journeyKey(t, m, "enter")
			r = m.journeyRecord()
			if r.receipt == nil || r.receipt.Identity.MissionID != second.ID || !bytes.Equal(r.receipt.Content, source) || posts != 2 {
				t.Fatal("real exact approval failed", m.View().Content)
			}
			disk, err := os.ReadFile(filepath.Join(ws.Root, path))
			if err != nil || !bytes.Equal(disk, source) {
				t.Fatal("approval changed source")
			}
			other, err := st.GetMission(first.ID)
			if err != nil || other.Binding != nil {
				t.Fatal("two missions mixed", other, err)
			}
			if m.link != nil || m.termMode {
				t.Fatal("planning implicitly attached/input")
			}
		})
	}
}
