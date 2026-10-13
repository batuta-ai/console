package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthRoundTrip(t *testing.T) {
	dir := runtimeTestHome(t)
	path := filepath.Join(dir, "auth.secret")
	a, err := LoadOrCreateAuth(path)
	if err != nil || len(a) < 32 {
		t.Fatalf("create: %q %v", a, err)
	}
	b, err := LoadOrCreateAuth(path)
	if err != nil || a != b {
		t.Fatalf("reload %q vs %q %v", b, a, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeFileProtection(t, path, st)
}

func TestLoopbackListen(t *testing.T) {
	dir := runtimeTestHome(t)
	if _, err := Resolve(dir, "0.0.0.0:9477"); err == nil {
		t.Fatal("non-loopback listen must fail")
	}
	if _, err := Resolve(dir, "127.0.0.1:9477"); err != nil {
		t.Fatal(err)
	}
}

func TestLockExclusive(t *testing.T) {
	dir := runtimeTestHome(t)
	path := filepath.Join(dir, "consoled.lock")
	f, err := WriteLock(path, LockInfo{PID: os.Getpid(), Addr: "127.0.0.1:1", Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := WriteLock(path, LockInfo{PID: 1}); err == nil {
		t.Fatal("expected exclusive lock to fail")
	}
}
