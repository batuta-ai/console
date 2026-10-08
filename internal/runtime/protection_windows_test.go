package runtime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRuntimeProtectionRefusesWithoutMutation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "absent"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "console space ç")
			tokenPath := filepath.Join(home, "auth.secret")
			lockPath := filepath.Join(home, "consoled.lock")
			tokenBytes := []byte(strings.Repeat("synthetic-only-", 4))
			lockBytes := []byte(`{"pid":123,"addr":"127.0.0.1:9477","home":"synthetic"}`)
			if existing {
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tokenPath, tokenBytes, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(lockPath, lockBytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if token, err := LoadOrCreateAuth(tokenPath); token != "" || !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("auth must refuse: token length=%d error=%v", len(token), err)
			}
			if lock, err := WriteLock(lockPath, LockInfo{PID: os.Getpid()}); lock != nil || !errors.Is(err, errors.ErrUnsupported) {
				if lock != nil {
					lock.Close()
				}
				t.Fatalf("lock must refuse: %v", err)
			}
			if paths, err := Resolve(home, "127.0.0.1:0"); paths != (Paths{}) || !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("runtime must refuse: %+v %v", paths, err)
			}
			if !existing {
				if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refusal created home: %v", err)
				}
				return
			}
			for path, want := range map[string][]byte{tokenPath: tokenBytes, lockPath: lockBytes} {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("refusal changed %s: %v", path, err)
				}
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 2 {
				t.Fatalf("refusal created artifacts: %v %v", entries, err)
			}
		})
	}
}

func TestWindowsReadLockPreservesIdentityAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock with space.json")
	raw := []byte(`{"pid":42,"addr":"127.0.0.1:9477","home":"C:\\synthetic ç\\console"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLock(path)
	if err != nil || got.PID != 42 || got.Addr != "127.0.0.1:9477" || got.Home != `C:\synthetic ç\console` {
		t.Fatalf("lock identity: %+v %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("read mutated lock: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLock(path); err == nil {
		t.Fatal("malformed lock accepted")
	}
	if _, err := ReadLock(path + ".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing lock: %v", err)
	}
}
