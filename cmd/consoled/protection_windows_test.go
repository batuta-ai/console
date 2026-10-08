package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/runtime"
)

func TestWindowsDaemonRefusesBeforeOpeningState(t *testing.T) {
	home := t.TempDir()
	paths := runtime.Paths{Home: home, Auth: filepath.Join(home, "auth.secret"), Lock: filepath.Join(home, "consoled.lock"), DB: filepath.Join(home, "console.db"), Logs: filepath.Join(home, "logs"), Listen: "127.0.0.1:0"}
	if err := runDaemon(paths); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("daemon protection refusal: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("daemon wrote state before refusal: %v %v", entries, err)
	}
}
