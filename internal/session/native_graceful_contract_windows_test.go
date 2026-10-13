package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/accounts"
)

func TestTask43WindowsGracefulCommandEnvironmentStreams(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	runner.Graceful = true
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-must-not-cross")
	literal := "literal & % ! ç \" \\"
	command := task43WindowsNativeCommand(t, "environment", literal)
	command.Directory = t.TempDir()
	command.Stdin = []byte{'A', 0, '\r', '\n', 'Z'}
	command.Environment = []string{"home=foreign", "CoDeX_HoMe=foreign", "ANTHROPIC_API_KEY=synthetic-must-not-cross"}
	result, err := runner.Run(context.Background(), command)
	if err != nil || result.ExitCode != 0 {
		t.Fatal("native command failed", result, err)
	}
	var values map[string]string
	if err := json.Unmarshal(result.Stdout, &values); err != nil {
		t.Fatal(err)
	}
	if values["home"] != accounts.ExplicitHome(runner.Environment) || values["home"] == "foreign" || values["codex"] == "foreign" || values["secret"] != "" || values["arg"] != literal || values["input"] != string(command.Stdin) || !strings.EqualFold(values["cwd"], command.Directory) || string(result.Stderr) != "native-error-stream" {
		t.Fatal("native command environment/stdin/argv/cwd/stream contract", values, result)
	}
	result, err = runner.Run(context.Background(), task43WindowsNativeCommand(t, "nonzero"))
	if err != nil || result.ExitCode != 44 || string(result.Stdout) != "native-failure-stdout" || string(result.Stderr) != "native-failure-stderr" {
		t.Fatal("native command failure output contract", result, err)
	}
}

func TestTask43WindowsGracefulCommandOutputObservers(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	runner.Graceful = true
	command := task43WindowsNativeCommand(t, "large")
	var stdout, stderr bytes.Buffer
	command.StdoutLimit, command.StderrLimit = 128, 97
	command.Observer, command.StderrObserver = &stdout, &stderr
	result, err := runner.Run(context.Background(), command)
	if err != nil || result.ExitCode != 0 || len(result.Stdout) != 128 || len(result.Stderr) != 97 || !result.StdoutTruncated || !result.StderrTruncated || stdout.Len() != 64<<10 || stderr.Len() != 64<<10 || !bytes.Equal(result.Stdout, bytes.Repeat([]byte{'x'}, 128)) || !bytes.Equal(result.Stderr, bytes.Repeat([]byte{'z'}, 97)) {
		t.Fatal("native command changed bounded capture or complete observer streams", result, stdout.Len(), stderr.Len(), err)
	}
}

func TestTask43WindowsGracefulCommandClosedAdmission(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	runner.Graceful = true
	runner.Manager.CloseAdmission()
	result, err := runner.Run(context.Background(), task43WindowsNativeCommand(t, "nonzero"))
	if !errors.Is(err, ErrProbeClosing) || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatal("closed native admission allowed command execution", result, err)
	}
}
