package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/testfixture"
)

func TestVersionWithoutInitialization(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, testfixture.ExecutableName("binary"))
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	home := filepath.Join(dir, "nonexistent")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version", "--home", home, "--listen", "invalid", "--core", filepath.Join(dir, "missing-core"))
	cmd.Env = []string{"HOME=" + home, "PATH=" + dir}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v: %s", err, output)
	}
	var got struct {
		Binary    string
		GoVersion string `json:"go_version"`
		Release   struct{ Version, Revision, Authority string }
		Observed  struct{ Version, Revision, Authority string }
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("JSON: %v: %s", err, output)
	}
	if got.Binary != "consoled" || got.GoVersion == "" || got.Release.Version == "" || got.Release.Revision == "" || got.Observed.Authority != "go_build_info" {
		t.Fatalf("%s", output)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home created: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != testfixture.ExecutableName("binary") {
		t.Fatalf("filesystem changed: %v %v", entries, err)
	}
}
