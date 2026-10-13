package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/adapters"
	consolert "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func TestNativeOriginalAuthenticationConstructor(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			svc, _ := persistenceSvc(t)
			a, err := svc.CreateAccount("synthetic-constructor", id)
			if err != nil {
				t.Fatal(err)
			}
			a, err = svc.ConnectAccount(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			ad, err := adapters.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			config, name := ".codex", "auth.json"
			if id == "claude" {
				config, name = ".claude", ".credentials.json"
			}
			path := filepath.Join(ad.IsolateDir(svc.Home, a.ID), config, name)
			var security string
			for _, identity := range []string{"A", "B"} {
				authenticateFixtureAccount(t, svc, a, identity)
				file, err := consolert.OpenPrivateFile(path)
				if err != nil {
					t.Fatal("original constructor did not produce native private credential", err)
				}
				sd, securityErr := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
				if err := errors.Join(securityErr, file.Close()); err != nil {
					t.Fatal(err)
				}
				if security != "" && sd.String() != security {
					t.Fatal("original repeated credential writing changed security")
				}
				security = sd.String()
				data, err := os.ReadFile(path)
				var payload map[string]any
				if err != nil || json.Unmarshal(data, &payload) != nil || payload["synthetic_identity"] != identity {
					t.Fatal("original synthetic credential identity/bytes missing", err)
				}
				if id == "codex" && payload["auth_mode"] != "chatgpt" || id == "claude" && payload["claudeAiOauth"] == nil {
					t.Fatal("original provider credential shape changed")
				}
			}
		})
	}
}
