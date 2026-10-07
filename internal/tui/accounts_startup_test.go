package tui

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestAccountNonTTYEmptyStartup(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("BATUTA_LANG", "en")
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) { accountCatalog(w, r, nil) })
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = previous; _ = read.Close(); _ = write.Close() }()
	if err := Run(c); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "n new account (Codex / Claude)") {
		t.Fatal(string(out))
	}
}
