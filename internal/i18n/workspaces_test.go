package i18n

import (
	"strings"
	"testing"
)

func TestWorkspaceTranslationsComplete(t *testing.T) {
	count := 0
	for key, value := range en {
		if key != "workspaces" && !strings.HasPrefix(key, "workspace_") {
			continue
		}
		count++
		if strings.TrimSpace(value) == "" || strings.TrimSpace(pt[key]) == "" {
			t.Errorf("missing workspace translation %q", key)
		}
	}
	if count < 60 {
		t.Fatalf("expected workspace fields, states, errors and recovery hints; got %d", count)
	}
	for key := range pt {
		if strings.HasPrefix(key, "workspace_") && en[key] == "" {
			t.Errorf("missing English workspace translation %q", key)
		}
	}
}
