package testfixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	consolert "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

func RewriteScript(t testing.TB, path string, body []byte, mode os.FileMode) error {
	t.Helper()
	if err := RemoveScript(t, path); err != nil {
		return err
	}
	return WriteScript(t, path, body, mode)
}

// RemoveScript is for sequential owned fixture setup, after any process using it
// has joined. Validation precedes deletion; deletion of the triple is not atomic.
func RemoveScript(t testing.TB, path string) (err error) {
	t.Helper()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return os.ErrInvalid
	}
	parent, err := consolert.OpenPrivateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	paths := []string{path, path + ".shell-script", path + ".shell-config.json"}
	files := []*os.File{}
	closeFiles := func() error {
		var err error
		for _, file := range files {
			err = errors.Join(err, file.Close())
		}
		files = nil
		return err
	}
	defer func() { err = errors.Join(err, closeFiles()) }()
	contents := make([][]byte, len(paths))
	for i, target := range paths {
		file, err := consolert.OpenPrivateFile(target)
		if err != nil {
			return err
		}
		files = append(files, file)
		sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		control, _, err := sd.Control()
		if err != nil {
			return err
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			return os.ErrPermission
		}
		limit := int64(64 << 20)
		if i == 2 {
			limit = 64 << 10
		}
		contents[i], err = io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil {
			return err
		}
		if int64(len(contents[i])) > limit {
			return os.ErrInvalid
		}
	}
	var descriptor nativeScriptDescriptor
	decoder := json.NewDecoder(bytes.NewReader(contents[2]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return os.ErrInvalid
	}
	if nativeFixtureGitErr != nil {
		return nativeFixtureGitErr
	}
	shell := filepath.Join(filepath.Dir(filepath.Dir(nativeFixtureGit)), "usr", "bin", "sh.exe")
	shellBytes, err := os.ReadFile(shell)
	if err != nil {
		return err
	}
	image, err := nativeScriptExecutable(t)
	if err != nil {
		return err
	}
	want := nativeScriptDescriptor{Executable: path, ImageSHA: fixtureDigest(image), Shell: shell, ShellSHA: fixtureDigest(shellBytes), Script: paths[1], ScriptSHA: fixtureDigest(contents[1])}
	canonical, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if descriptor != want || !bytes.Equal(contents[0], image) || !bytes.Equal(contents[2], canonical) {
		return os.ErrPermission
	}
	if err := closeFiles(); err != nil {
		return err
	}
	for _, target := range paths {
		if err := os.Remove(target); err != nil {
			return err
		}
	}
	return nil
}
