package adapters_test

import (
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	consolert "github.com/batuta-ai/console/internal/runtime"
)

func TestNativeOriginalProviderAuthenticationConstructor(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			_, _, home, account, token := authFixture(t, id, nil)
			config, name := ".codex", "auth.json"
			if id == "claude" {
				config, name = ".claude", ".credentials.json"
			}
			file, err := consolert.OpenPrivateFile(filepath.Join(home, "accounts", account.ID, config, name))
			if err != nil {
				t.Fatal("original provider constructor did not produce native private credential", err)
			}
			data, err := io.ReadAll(file)
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				AuthMode string `json:"auth_mode"`
				Tokens   struct {
					Access string `json:"access_token"`
				} `json:"tokens"`
				OAuth struct {
					Access string `json:"accessToken"`
				} `json:"claudeAiOauth"`
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			if id == "codex" && (payload.AuthMode != "chatgpt" || payload.Tokens.Access != token) || id == "claude" && payload.OAuth.Access != token {
				t.Fatal("original provider constructor changed synthetic token bytes")
			}
		})
	}
}
