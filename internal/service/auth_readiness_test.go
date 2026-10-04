package service

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestOfficialSessionRequiresAuthentication(t *testing.T) {
	for _, connected := range []bool{false, true} {
		name := "disconnected"
		if connected {
			name = "connected-without-authentication"
		}
		t.Run(name, func(t *testing.T) {
			f, cli, out := commandFixture(t, "codex")
			if err := os.Remove(filepath.Join(f.svc.Home, "accounts", f.account.ID, ".codex", "auth.json")); err != nil {
				t.Fatal(err)
			}
			f.account.Connected = connected
			if err := f.svc.Store.PutAccount(f.account); err != nil {
				t.Fatal(err)
			}
			// Explicit custom commands remain a credential-free compatibility path.
			off := false
			control, err := f.svc.StartSession(StartSessionIn{
				MissionID: f.mission.ID, AccountID: f.account.ID,
				Inject: &off, Argv: []string{cli, "synthetic custom control"},
			})
			if err != nil {
				t.Fatal(err)
			}
			observedCommand(t, f, control, out, []string{cli, "synthetic custom control"})
			if _, err := f.svc.StopSession(control.ID); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(out); err != nil {
				t.Fatal(err)
			}
			before, err := f.svc.Store.ListSessions()
			if err != nil {
				t.Fatal(err)
			}
			seq, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			launched, startErr := f.svc.StartSession(StartSessionIn{
				MissionID: f.mission.ID, AccountID: f.account.ID, Inject: &off,
			})
			if launched.PID != 0 {
				if _, err := f.svc.StopSession(launched.ID); err != nil {
					t.Error(err)
				}
			}
			if startErr == nil {
				t.Error("official provider session started without authentication")
			}
			after, err := f.svc.Store.ListSessions()
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Errorf("accepted sessions changed: before=%d after=%d", len(before), len(after))
			}
			events, err := f.svc.Store.EventsSince(seq, 1000)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Kind == "session.started" {
					t.Errorf("unexpected accepted provider start: %v", event)
				}
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("unexpected provider child artifact: %v", err)
			}
			if got := f.svc.DecorateAccount(domain.Account{ID: f.account.ID, Adapter: "codex", Connected: connected}); got.Authed {
				t.Error("unverified account reported authenticated")
			}
		})
	}
}

func TestStructuralDraftDoesNotPersistAuthentication(t *testing.T) {
	for _, state := range []string{"authenticated", "missing-credential", "provider-outage"} {
		t.Run(state, func(t *testing.T) {
			f, _, _ := commandFixture(t, "codex")
			if state == "missing-credential" {
				if err := os.Remove(filepath.Join(f.svc.Home, "accounts", f.account.ID, ".codex", "auth.json")); err != nil {
					t.Fatal(err)
				}
			} else if state == "provider-outage" {
				authenticateFixtureAccountWithStatus(t, f.svc, f.account, "A", http.StatusServiceUnavailable)
			}
			q := domain.Squad{Name: "structural", Members: []domain.Member{{ID: "maestro", Role: "Maestro", Maestro: true, CLI: "codex", AccountID: f.account.ID}}}
			saved, err := f.svc.SaveSquad(q)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := f.svc.Store.GetSquad(saved.ID)
			if err != nil || saved.Draft || stored.Draft {
				t.Fatalf("transient authentication persisted as structural draft: %+v %+v %v", saved, stored, err)
			}
			v := f.svc.ValidateDraft(stored)
			if v.Ready != (state == "authenticated") {
				t.Fatalf("fresh execution readiness ignored credential/provider state: %+v", v)
			}
		})
	}
}

func TestInvalidCredentialsRefuseActualOfficialLaunch(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, state := range []string{"expired", "revoked"} {
			t.Run(adapter+"/"+state, func(t *testing.T) {
				f, _, out := commandFixture(t, adapter)
				want := "provider_rejected"
				if state == "revoked" {
					authenticateFixtureAccountWithStatus(t, f.svc, f.account, "A", http.StatusUnauthorized)
				} else {
					want = "credential_expired"
					config, name := ".claude", ".credentials.json"
					data := []byte(`{"claudeAiOauth":{"accessToken":"synthetic","expiresAt":1,"scopes":["user:inference","user:profile"]}}`)
					if adapter == "codex" {
						config, name = ".codex", "auth.json"
						data = []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"eyJhbGciOiJub25lIn0.eyJleHAiOjF9.synthetic","account_id":"synthetic"}}`)
					}
					if err := os.WriteFile(filepath.Join(f.svc.Home, "accounts", f.account.ID, config, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				off := false
				if live, err := f.svc.StartSession(StartSessionIn{MissionID: f.mission.ID, AccountID: f.account.ID, Adapter: adapter, Inject: &off}); err == nil {
					f.svc.StopSession(live.ID)
					t.Fatal("invalid credential reached official launch")
				} else if code, ok := ReadinessFailure(err); !ok || code != want {
					t.Fatalf("wrong credential recovery diagnostic: %v", err)
				}
				assertNoCommandLaunch(t, f, out)
			})
		}
	}
}

func TestInitPreservesProviderFailureDiagnostic(t *testing.T) {
	f, _, out := commandFixture(t, "codex")
	authenticateFixtureAccountWithStatus(t, f.svc, f.account, "A", http.StatusServiceUnavailable)
	if _, err := f.svc.StartInit(f.workspace.ID, "synthetic", "web", "test", confirmedFixtureInput(f.account.ID)); err == nil {
		t.Fatal("provider outage authorized init")
	} else if code, ok := ReadinessFailure(err); !ok || code != "provider_unavailable" || !strings.Contains(err.Error(), f.account.ID) {
		t.Fatalf("provider outage or selected account was misreported: %v", err)
	}
	assertNoCommandLaunch(t, f, out)
}

func TestDraftReadinessRequiresAuthenticatedCompatibleAccount(t *testing.T) {
	for _, state := range []string{"missing-credential", "missing-cli", "disconnected", "wrong-adapter", "unsupported", "isolation-pending"} {
		t.Run(state, func(t *testing.T) {
			f, cli, _ := commandFixture(t, "codex")
			member := domain.Member{ID: "maestro", Role: "Maestro", Maestro: true, CLI: "codex", AccountID: f.account.ID}
			switch state {
			case "missing-credential":
				if err := os.Remove(filepath.Join(f.svc.Home, "accounts", f.account.ID, ".codex", "auth.json")); err != nil {
					t.Fatal(err)
				}
			case "missing-cli":
				if err := os.Remove(cli); err != nil {
					t.Fatal(err)
				}
			case "disconnected":
				f.account.Connected = false
			case "wrong-adapter":
				member.CLI = "claude"
			case "unsupported":
				member.CLI, f.account.Adapter = "gemini", "gemini"
			case "isolation-pending":
				f.account.Isolation = domain.IsolationNotYetQualified
			}
			if err := f.svc.Store.PutAccount(f.account); err != nil {
				t.Fatal(err)
			}
			got := f.svc.ValidateDraft(domain.Squad{Members: []domain.Member{member}})
			if got.Ready {
				t.Fatalf("unsafe squad marked ready (%s): %+v", state, got)
			}
		})
	}
}
