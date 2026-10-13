package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestNativeCredentialSessionSetupFaultAuthority(t *testing.T) {
	root := testfixture.PrivateHome(t)
	blocked := filepath.Join(root, "actual cache is a regular file")
	payload := []byte("owned cache-root blocker")
	if err := os.WriteFile(blocked, payload, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(blocked)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("actual cache blocker is not a regular file", err)
	}
	key := credentialSessionCacheKey()
	previous := os.Getenv(key)
	t.Setenv(key, blocked)
	cache, cacheErr := os.UserCacheDir()
	env, home, setupErr := accounts.SessionEnvironment()
	t.Logf("actual cache authority key=%s cache=%s cacheErr=%v envCount=%d home=%s setupErr=%v", key, cache, cacheErr, len(env), home, setupErr)
	if home != "" {
		if err := os.RemoveAll(home); err != nil {
			t.Fatal("remove actual owned unexpected home", err)
		}
	}
	if setupErr == nil {
		t.Fatal("blocked native cache setup reached a real private home")
	}
	if cacheErr != nil || cache != blocked || home != "" || len(env) != 0 {
		t.Fatal("native cache fault did not refuse the blocked authority", cacheErr, setupErr)
	}
	t.Setenv(key, previous)
	cache, err = os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	env, home, err = accounts.SessionEnvironment()
	if err != nil {
		t.Fatal("restored native cache failed to create a home", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error("remove recovered owned home", err)
		}
	})
	parent := filepath.Join(cache, "Batuta", "Console", "session-homes")
	if filepath.Dir(home) != parent || !strings.HasPrefix(filepath.Base(home), "session-") || len(env) == 0 {
		t.Fatal("recovered home used a different native cache authority", home)
	}
	info, err = os.Stat(home)
	if err != nil || !credentialPrivateDirectory(home, info) {
		t.Fatal("recovered native home is not private", err)
	}
	info, err = os.Lstat(blocked)
	data, readErr := os.ReadFile(blocked)
	if err != nil || !info.Mode().IsRegular() || readErr != nil || !bytes.Equal(data, payload) {
		t.Fatal("cache fault mutated the owned blocker", err, readErr)
	}
	t.Logf("actual native cache fault refused before home; recovered home=%s envCount=%d blocker bytes/type intact", home, len(env))
}
