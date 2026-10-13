package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/publication"
)

func TestTask43WindowsNativeCommandFixture(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "task43-native-command" {
		return
	}
	switch os.Args[4] {
	case "environment":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{"home": os.Getenv("HOME"), "codex": os.Getenv("CODEX_HOME"), "secret": os.Getenv("ANTHROPIC_API_KEY"), "arg": os.Args[5], "input": string(input), "cwd": cwd}
		if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(os.Stderr, "native-error-stream")
		os.Exit(0)
	case "nonzero":
		fmt.Fprint(os.Stdout, "native-failure-stdout")
		fmt.Fprint(os.Stderr, "native-failure-stderr")
		os.Exit(44)
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 64<<10))
		fmt.Fprint(os.Stderr, strings.Repeat("z", 64<<10))
		os.Exit(0)
	default:
		t.Fatal("unknown native command fixture")
	}
}

func task43WindowsNativeCommand(t *testing.T, mode string, rest ...string) publication.Command {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return publication.Command{Executable: exe, Args: append([]string{"-test.run=^TestTask43WindowsNativeCommandFixture$", "--", "task43-native-command", mode}, rest...)}
}

func task43WindowsNativeRunner(t *testing.T) NativeRunner {
	t.Helper()
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	m := New(t.TempDir())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return NativeRunner{Manager: m, Environment: env}
}

func TestTask43WindowsNativeCommandEnvironmentStreams(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
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

func TestTask43WindowsNativeCommandOutputObservers(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	command := task43WindowsNativeCommand(t, "large")
	var stdout, stderr bytes.Buffer
	command.StdoutLimit, command.StderrLimit = 128, 97
	command.Observer, command.StderrObserver = &stdout, &stderr
	result, err := runner.Run(context.Background(), command)
	if err != nil || result.ExitCode != 0 || len(result.Stdout) != 128 || len(result.Stderr) != 97 || !result.StdoutTruncated || !result.StderrTruncated || stdout.Len() != 64<<10 || stderr.Len() != 64<<10 || !bytes.Equal(result.Stdout, bytes.Repeat([]byte{'x'}, 128)) || !bytes.Equal(result.Stderr, bytes.Repeat([]byte{'z'}, 97)) {
		t.Fatal("native command changed bounded capture or complete observer streams", result, stdout.Len(), stderr.Len(), err)
	}
}

func TestTask43WindowsNativeCommandClosedAdmission(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	runner.Manager.CloseAdmission()
	result, err := runner.Run(context.Background(), task43WindowsNativeCommand(t, "nonzero"))
	if !errors.Is(err, ErrProbeClosing) || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatal("closed native admission allowed command execution", result, err)
	}
}
