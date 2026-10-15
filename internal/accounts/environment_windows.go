package accounts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
)

func environmentKey(key string) string { return strings.ToUpper(key) }
func platformRuntimeKey(key string) bool {
	switch key {
	case "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC":
		return true
	}
	return false
}
func platformIdentityKey(key string) bool {
	switch key {
	case "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP", "HOMEDRIVE", "HOMEPATH":
		return true
	}
	return false
}
func platformIdentityPaths(home string) map[string]string {
	return map[string]string{"USERPROFILE": home, "APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "AppData", "Local"), "TEMP": filepath.Join(home, "tmp"), "TMP": filepath.Join(home, "tmp")}
}
func platformHomeMetadata(home string) []string {
	volume := filepath.VolumeName(home)
	return []string{"HOMEDRIVE=" + volume, "HOMEPATH=" + strings.TrimPrefix(home, volume)}
}
func newSessionHome() (string, error) {
	local, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	parent := filepath.Join(local, "Batuta", "Console", "session-homes")
	// GetTempPathW reserves MAX_PATH and can loop during descendant creation
	// with an overlong TMP. Reserve its trailing separator and terminating NUL.
	prospective := filepath.Join(parent, "session-"+strings.Repeat("0", 32), "tmp")
	if err := windowsTemporaryPathLength(prospective); err != nil {
		return "", err
	}
	if err := consoleruntime.EnsurePrivateDirectory(parent); err != nil {
		return "", err
	}
	// Resolve filesystem spelling after private validation; NewPrivateTemp
	// revalidates the canonical parent before creating the owned home.
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	prospective = filepath.Join(parent, "session-"+strings.Repeat("0", 32), "tmp")
	if err := windowsTemporaryPathLength(prospective); err != nil {
		return "", err
	}
	return consoleruntime.NewPrivateTemp(parent, "session-")
}
func createIdentityDirectory(path string) error { return consoleruntime.EnsurePrivateDirectory(path) }

func windowsTemporaryPathLength(path string) error {
	if !filepath.IsAbs(path) || len(utf16.Encode([]rune(path))) > 258 {
		return fmt.Errorf("Windows temporary directory exceeds the native path contract: %w", os.ErrInvalid)
	}
	return nil
}

// ValidateWindowsTemporaryEnvironment refuses legacy path overflow and unsafe
// selected temporary directories before the Windows launcher creates a root.
func ValidateWindowsTemporaryEnvironment(env []string) error {
	paths := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		key = environmentKey(key)
		if ok && (key == "TEMP" || key == "TMP") {
			paths[key] = value
		}
	}
	if len(paths) != 2 {
		return fmt.Errorf("Windows temporary identity is incomplete: %w", os.ErrInvalid)
	}
	for _, path := range paths {
		if err := windowsTemporaryPathLength(path); err != nil {
			return err
		}
		dir, err := consoleruntime.OpenPrivateDirectory(path)
		if err != nil {
			return err
		}
		if err := dir.Close(); err != nil {
			return err
		}
	}
	return nil
}
