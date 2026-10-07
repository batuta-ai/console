package i18n

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Supplementary pack integrity check, separate from catalog behavioral TDD.
func TestPacksHaveMatchingKeysAndPlaceholders(t *testing.T) {
	placeholders := regexp.MustCompile(`\{[A-Za-z_][A-Za-z_0-9]*\}|%[+#.0-9-]*[sdvq]`)
	for key, english := range en {
		portuguese, ok := pt[key]
		if !ok || strings.TrimSpace(english) == "" || strings.TrimSpace(portuguese) == "" {
			t.Errorf("missing or empty translation %s", key)
		}
		left, right := placeholders.FindAllString(english, -1), placeholders.FindAllString(portuguese, -1)
		sort.Strings(left)
		sort.Strings(right)
		if !reflect.DeepEqual(left, right) {
			t.Errorf("placeholder mismatch %s: %v / %v", key, left, right)
		}
	}
	for key := range pt {
		if _, ok := en[key]; !ok {
			t.Errorf("Portuguese-only key %s", key)
		}
	}
}
