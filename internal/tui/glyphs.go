package tui

import (
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

func asciiMode() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("BATUTA_ASCII") == "1" || os.Getenv("TERM") == "dumb" {
		return true
	}
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return true
	}
	loc := os.Getenv("LC_ALL")
	if loc == "" {
		loc = os.Getenv("LANG")
	}
	l := strings.ToLower(loc)
	if loc != "" && !strings.Contains(l, "utf-8") && !strings.Contains(l, "utf8") {
		return true
	}
	return false
}

func asciiFold(s string) string {
	if !asciiMode() {
		return s
	}
	return strings.NewReplacer(
		"—", "-",
		"─", "-",
		"✓", "+",
		"✗", "x",
		"·", ".",
		"…", "~",
	).Replace(s)
}

func rule(width int) string {
	if width < 20 {
		width = 20
	}
	ch := "─"
	if asciiMode() {
		ch = "-"
	}
	return strings.Repeat(ch, width)
}
