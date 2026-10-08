package accounts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
)

func TestTask43WindowsSessionTemporaryLengthBoundary(t *testing.T) {
	local := task43AccountHome(t)
	for len(utf16.Encode([]rune(local))) <= 280 {
		local = filepath.Join(local, "long-temporary-component-"+strings.Repeat("x", 20))
	}
	if err := consoleruntime.EnsurePrivateDirectory(local); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", local)
	task43AccountACL(t, local)
	before, err := os.ReadDir(local)
	if err != nil || len(before) != 0 {
		t.Fatal("long selected cache fixture", err)
	}
	env, home, err := SessionEnvironment()
	if home != "" {
		defer os.RemoveAll(home)
	}
	if err == nil || env != nil || home != "" {
		t.Errorf("overlong Windows temporary path was admitted: error=%v home=%q", err, home)
	}
	after, readErr := os.ReadDir(local)
	if readErr != nil || len(after) != 0 {
		t.Errorf("refusal created artifacts in the selected cache: entries=%d error=%v", len(after), readErr)
	}
}
