package i18n

import (
	"strings"
	"testing"
)

func TestAccountPacksAndFocusedHints(t *testing.T) {
	for key, value := range en {
		if strings.HasPrefix(key, "account_") && (value == "" || pt[key] == "" || pt[key] == value) {
			t.Errorf("missing translated account key %s", key)
		}
	}
	for key := range pt {
		if _, ok := en[key]; !ok {
			t.Errorf("missing English key %s", key)
		}
	}
	for _, lang := range []string{"en", "pt"} {
		t.Setenv("BATUTA_LANG", lang)
		if T("account_empty") == "" {
			t.Error("missing empty-account onboarding")
		}
		if strings.Contains(T("term"), "Esc") || !strings.Contains(T("term"), "Ctrl-]") {
			t.Error("terminal hint must agree with child focus")
		}
	}
}
