package accounts

import (
	"os"
	"path/filepath"
	"strings"

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
	if err := consoleruntime.EnsurePrivateDirectory(parent); err != nil {
		return "", err
	}
	return consoleruntime.NewPrivateTemp(parent, "session-")
}
func createIdentityDirectory(path string) error { return consoleruntime.EnsurePrivateDirectory(path) }
