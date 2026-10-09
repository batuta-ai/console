package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A copied test PE is the local selected CLI fixture. Core's public inventory
// adapter invokes its native diagnostic argv before any test flags exist.
func TestMain(m *testing.M) {
	if strings.EqualFold(filepath.Base(os.Args[0]), "codex.exe") && len(os.Args) > 1 {
		var output string
		switch strings.Join(os.Args[1:], " ") {
		case "--version":
			output = "codex-cli 0.160.0"
		case "debug models", "debug models --bundled":
			output = `{"models":[{"slug":"qa-native"}]}`
		case "doctor --json --summary":
			output = `{"schema_version":1}`
		case "plugin list --json", "plugin marketplace list --json":
			output = `{}`
		}
		if output != "" {
			if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
				os.Exit(90)
			}
			f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), "native-inventory-probes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(91)
			}
			_, err = fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			if err != nil || f.Close() != nil {
				os.Exit(92)
			}
			fmt.Println(output)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
