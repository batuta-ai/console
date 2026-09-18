package hostfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const prompt = "Escolha a pasta do workspace"

const darwinScript = `POSIX path of (choose folder with prompt "Escolha a pasta do workspace")`

const windowsScript = `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Escolha a pasta do workspace'
$d.ShowNewFolderButton = $true
if ($d.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { exit 1 }
Write-Output $d.SelectedPath
`

// PickDirectory opens a native folder dialog and returns an absolute path.
// canceled is true when the operator dismisses the dialog.
func PickDirectory(ctx context.Context) (string, bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return run(ctx, "osascript", "-e", darwinScript)
	case "windows":
		return run(ctx, "powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", windowsScript)
	default:
		if _, err := exec.LookPath("zenity"); err == nil {
			return run(ctx, "zenity", "--file-selection", "--directory", "--title="+prompt)
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			start := os.Getenv("HOME")
			if start == "" {
				start = "/"
			}
			return run(ctx, "kdialog", "--getexistingdirectory", start, prompt)
		}
		return "", false, fmt.Errorf("no directory picker (install zenity or kdialog)")
	}
}

func run(ctx context.Context, name string, args ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if isCanceled(err, stderr.String()) {
			return "", true, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", false, errors.New(msg)
	}
	path := normalizePicked(stdout.String())
	if path == "" || path == "." {
		return "", false, fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	return abs, false, nil
}

func normalizePicked(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, `"'`)
	if runtime.GOOS != "windows" {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func isCanceled(err error, stderr string) bool {
	if err == nil {
		return false
	}
	blob := strings.ToLower(stderr + "\n" + err.Error())
	if strings.Contains(blob, "user canceled") || strings.Contains(blob, "user cancelled") {
		return true
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}
