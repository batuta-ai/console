package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
)

func validIsolate(consoleHome, accountID string) bool {
	clean := filepath.Clean(consoleHome)
	return filepath.IsAbs(consoleHome) && filepath.Dir(clean) != clean && accountID != "" && accountID != "." && accountID != ".." && !strings.ContainsAny(accountID, "/\\\x00")
}

func checkedDirectory(path string) (os.FileInfo, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("isolation path must be a directory without symlinks: %s", path)
	}
	return info, nil
}

func privateDirectory(path string) error {
	info, err := checkedDirectory(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0700 {
		return os.Chmod(path, 0700)
	}
	return nil
}

// IsolateEnv prepares account-owned identity paths and propagates failures.
// No directory or credential is copied from the daemon's personal home.
func (a Adapter) IsolateEnv(consoleHome string, account domain.Account) ([]string, error) {
	if !validIsolate(consoleHome, account.ID) {
		return nil, fmt.Errorf("absolute Console home and safe account ID required for isolation")
	}
	if account.Adapter != "" && account.Adapter != a.ID {
		return nil, fmt.Errorf("account adapter does not match %s", a.ID)
	}
	// The Console home may be new, but every controlled path below it is checked
	// before it can be used as a parent or environment pointer.
	if err := os.MkdirAll(filepath.Dir(consoleHome), 0700); err != nil {
		return nil, err
	}
	base := filepath.Join(consoleHome, "accounts")
	home := filepath.Join(base, account.ID)
	if _, err := checkedDirectory(consoleHome); err != nil {
		return nil, err
	}
	for _, path := range []string{base, home} {
		if err := privateDirectory(path); err != nil {
			return nil, err
		}
	}
	paths := []struct{ key, path string }{
		{"HOME", home}, {"TMPDIR", filepath.Join(home, "tmp")}, {"XDG_CONFIG_HOME", filepath.Join(home, ".config")}, {"", filepath.Join(home, ".local")}, {"XDG_DATA_HOME", filepath.Join(home, ".local", "share")}, {"XDG_CACHE_HOME", filepath.Join(home, ".cache")}, {"XDG_STATE_HOME", filepath.Join(home, ".local", "state")},
	}
	switch a.ID {
	case "codex":
		paths = append(paths, struct{ key, path string }{"CODEX_HOME", filepath.Join(home, ".codex")})
	case "claude":
		paths = append(paths, struct{ key, path string }{"CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")})
	case "cursor-agent":
		paths = append(paths, struct{ key, path string }{"CURSOR_CONFIG_DIR", filepath.Join(home, ".cursor")})
	}
	env := make([]string, 0, len(paths))
	for _, path := range paths {
		if err := privateDirectory(path.path); err != nil {
			return nil, err
		}
		if path.key != "" {
			env = append(env, path.key+"="+path.path)
		}
	}
	return env, nil
}
