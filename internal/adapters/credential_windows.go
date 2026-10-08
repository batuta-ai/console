package adapters

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
)

func checkedDirectory(path string) (os.FileInfo, error) {
	if err := consoleruntime.EnsurePrivateDirectory(path); err != nil {
		return nil, err
	}
	f, err := consoleruntime.OpenPrivateDirectory(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	return info, errors.Join(err, f.Close())
}
func privateDirectory(path string) error { return consoleruntime.EnsurePrivateDirectory(path) }
func prepareIsolateParent(string) error  { return nil }
func accountPlatformPaths(home string) map[string]string {
	return map[string]string{"USERPROFILE": home, "APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "AppData", "Local"), "TEMP": filepath.Join(home, "tmp"), "TMP": filepath.Join(home, "tmp")}
}
func accountPlatformMetadata(home string) []string {
	volume := filepath.VolumeName(home)
	return []string{"HOMEDRIVE=" + volume, "HOMEPATH=" + strings.TrimPrefix(home, volume)}
}
func identityDirectoryValid(path string, _ bool) bool {
	f, err := consoleruntime.OpenPrivateDirectory(path)
	return err == nil && f.Close() == nil
}
func readPrivateCredential(path string) (data []byte, err error) {
	f, err := consoleruntime.OpenPrivateFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			data = nil
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > 64<<10 {
		return nil, errors.New("invalid private credential file")
	}
	data, err = io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err == nil && (len(data) == 0 || len(data) > 64<<10) {
		return nil, errors.New("invalid private credential read")
	}
	return data, err
}
