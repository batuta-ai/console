package testfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var nativeFixtureGo, nativeFixtureGoErr = exec.LookPath("go")
var nativeScriptImageOnce sync.Once
var nativeScriptImage []byte
var nativeScriptImageErr error

type nativeScriptDescriptor struct {
	Executable string `json:"executable"`
	ImageSHA   string `json:"image_sha256"`
	Shell      string `json:"shell"`
	ShellSHA   string `json:"shell_sha256"`
	Script     string `json:"script"`
	ScriptSHA  string `json:"script_sha256"`
}

func fixtureDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func nativeScriptExecutable(t testing.TB) ([]byte, error) {
	t.Helper()
	nativeScriptImageOnce.Do(func() {
		if nativeFixtureGoErr != nil {
			nativeScriptImageErr = nativeFixtureGoErr
			return
		}
		_, source, _, ok := goruntime.Caller(0)
		if !ok {
			nativeScriptImageErr = fmt.Errorf("fixture source location unavailable")
			return
		}
		main := filepath.Join(filepath.Dir(source), "scriptcmd", "main_windows.go")
		output := filepath.Join(PrivateHome(t), "native-shell-fixture.exe")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, nativeFixtureGo, "build", "-o", output, main)
		cmd.Dir = filepath.Dir(source)
		cmd.WaitDelay = 2 * time.Second
		bytes, err := cmd.CombinedOutput()
		if err != nil {
			nativeScriptImageErr = fmt.Errorf("actual fixture build: %w: %s", err, bytes)
			return
		}
		nativeScriptImage, nativeScriptImageErr = os.ReadFile(output)
	})
	return nativeScriptImage, nativeScriptImageErr
}

func WriteScript(t testing.TB, path string, body []byte, _ os.FileMode) error {
	t.Helper()
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return os.ErrInvalid
	}
	scriptPath := path + ".shell-script"
	descriptorPath := path + ".shell-config.json"
	for _, target := range []string{path, scriptPath, descriptorPath} {
		if _, err := os.Lstat(target); err == nil {
			return os.ErrExist
		} else if !os.IsNotExist(err) {
			return err
		}
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
	descriptor := nativeScriptDescriptor{Executable: path, ImageSHA: fixtureDigest(image), Shell: shell, ShellSHA: fixtureDigest(shellBytes), Script: scriptPath, ScriptSHA: fixtureDigest(body)}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	for _, file := range []struct {
		path string
		data []byte
	}{{path, image}, {scriptPath, body}, {descriptorPath, data}} {
		if err := writePrivateFixtureFile(file.path, file.data); err != nil {
			return err
		}
	}
	t.Logf("actual native fixture image=%s imageSHA=%s shell=%s shellSHA=%s scriptSHA=%s compiler=%s", path, descriptor.ImageSHA, shell, descriptor.ShellSHA, descriptor.ScriptSHA, nativeFixtureGo)
	return nil
}
