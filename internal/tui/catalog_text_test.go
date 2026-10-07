package tui

import (
	"strings"
	"testing"
)

func TestCatalogPastedSettingsAreNotSilentlyTruncated(t *testing.T) {
	m := catalogSimple(New(nil).(model))
	m = catalogTabs(m, 1)
	text := strings.Repeat("objective ", 8000) + "final requirement"
	m = catalogPaste(m, text)
	if m.editor.Purpose != text {
		t.Fatal("purpose silently truncated instead of preserving user input")
	}
	m, _ = catalogKey(m, "esc")
	m, _ = catalogKey(m, "m")
	m, _ = catalogKey(m, "n")
	m = catalogTabs(m, 1)
	m = catalogPaste(m, text)
	if m.catalog.mission.brief != text {
		t.Fatal("mission brief silently truncated")
	}
}
