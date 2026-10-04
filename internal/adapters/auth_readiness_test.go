package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestCredentialPresenceDoesNotAuthenticate(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			ad, err := Get(id)
			if err != nil {
				t.Fatal(err)
			}
			consoleHome := filepath.Join(t.TempDir(), "console")
			account := domain.Account{ID: "qa-auth", Adapter: id, Connected: true}
			if _, err := ad.IsolateEnv(consoleHome, account); err != nil {
				t.Fatal(err)
			}
			config, filename := ".codex", "auth.json"
			if id == "claude" {
				config, filename = ".claude", ".credentials.json"
			}
			path := filepath.Join(ad.IsolateDir(consoleHome, account.ID), config, filename)
			expired := map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "console-synthetic-invalid-never-real",
				"expiresAt":   time.Now().Add(-time.Hour).UnixMilli(),
			}}
			if id == "codex" {
				expired = map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
					"access_token":  "console-synthetic-invalid-never-real",
					"id_token":      "console-synthetic-invalid-never-real",
					"refresh_token": "console-synthetic-invalid-never-real",
				}}
			}
			expiredJSON, err := json.Marshal(expired)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name string
				data []byte
			}{
				{"malformed", []byte("nonempty is not authentication")},
				{"empty-object", []byte("{}")},
				{"synthetic-identity", []byte(`{"synthetic_identity":"A"}`)},
				{"invalid-provider-credential", expiredJSON},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := os.WriteFile(path, tc.data, 0600); err != nil {
						t.Fatal(err)
					}
					if ad.LoggedIn(consoleHome, account) {
						t.Error("credential presence was reported as authenticated")
					}
				})
			}
		})
	}
}
