package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestGetCodex(t *testing.T) {
	a, err := Get("codex")
	if err != nil || a.Isolation != "supported" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestGetCursorAgent(t *testing.T) {
	a, err := Get("cursor-agent")
	if err != nil || a.CLI != "cursor-agent" || a.Isolation != domain.IsolationNotYetQualified {
		t.Fatalf("%+v %v", a, err)
	}
	ids := IDs()
	found := false
	for _, id := range ids {
		if id == "cursor-agent" {
			found = true
		}
	}
	if !found {
		t.Fatalf("IDs %v", ids)
	}
}

func TestUnknown(t *testing.T) {
	if _, err := Get("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestIsolateEnvDistinctHomes(t *testing.T) {
	home := t.TempDir()
	a, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	e1, err := a.IsolateEnv(home, domain.Account{ID: "acct_one"})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := a.IsolateEnv(home, domain.Account{ID: "acct_two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e1) < 3 || e1[0] == e2[0] {
		t.Fatalf("homes must differ: %v vs %v", e1, e2)
	}
	joined := strings.Join(e1, " ")
	if !strings.Contains(joined, "HOME=") || !strings.Contains(joined, "CODEX_HOME=") || !strings.Contains(joined, "acct_one") {
		t.Fatalf("%v", e1)
	}
}

func TestIsolateEnvCursorAgent(t *testing.T) {
	home := t.TempDir()
	a, err := Get("cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.IsolateEnv(home, domain.Account{ID: "acct_cur"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "CURSOR_CONFIG_DIR=") || !strings.Contains(joined, "acct_cur") {
		t.Fatalf("%v", env)
	}
}

func TestLoggedInReadsCodexAuthFile(t *testing.T) {
	home := t.TempDir()
	a, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	acct := domain.Account{ID: "acct_x", Adapter: "codex", Connected: true}
	if a.LoggedIn(home, acct) {
		t.Fatal("missing auth file should not count as login")
	}
	dir := a.IsolateDir(home, acct.ID)
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "auth.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.LoggedIn(home, acct) {
		t.Fatal("auth.json should count as login")
	}
}
