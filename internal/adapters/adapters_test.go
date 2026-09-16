package adapters

import (
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
	e1 := a.IsolateEnv(home, domain.Account{ID: "acct_one"})
	e2 := a.IsolateEnv(home, domain.Account{ID: "acct_two"})
	if len(e1) < 3 || e1[0] == e2[0] {
		t.Fatalf("homes must differ: %v vs %v", e1, e2)
	}
	joined := strings.Join(e1, " ")
	if !strings.Contains(joined, "HOME=") || !strings.Contains(joined, "CODEX_HOME=") || !strings.Contains(joined, "acct_one") {
		t.Fatalf("%v", e1)
	}
}
