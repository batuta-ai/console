package accounts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPartialSessionEnvironmentRemovesOwnedHome(t *testing.T) {
	home := sessionEnvironmentTestHome(t)
	if err := os.WriteFile(filepath.Join(home, ".config"), []byte("blocking fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := prepareSessionEnvironment(home)
	if err == nil || env != nil {
		t.Fatal("partial setup returned a usable environment")
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("partial setup retained its owned home: %v", err)
	}
}
