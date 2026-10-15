package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/publication"
)

func nativeCoreGitEnvironmentCommands(t *testing.T) (nativeCoreCommands, string, string, string) {
	t.Helper()
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	manager := session.New(home)
	t.Cleanup(func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	shell, err := nativeCoreShellPath()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Dir(shell) + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", path)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Neither the process nor the runner's base environment may supply Git config.
	ambient := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=proof.ambient", "GIT_CONFIG_VALUE_0=untrusted", "GIT_CONFIG_PARAMETERS='proof.parameters=untrusted'", "GIT_DIR=" + filepath.Join(home, "foreign-git"), "GIT_WORK_TREE=" + filepath.Join(home, "foreign-worktree")}
	for _, entry := range ambient {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("OPENAI_API_KEY", "synthetic-must-not-cross")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-must-not-cross")
	env = append(env, ambient...)
	env = append(env, "PATH="+path)
	return nativeCoreCommands{other: session.NativeRunner{Manager: manager, Environment: env}, active: &atomic.Bool{}}, home, shell, git
}

func TestNativeCoreGitEnvironmentLocalConfiguration(t *testing.T) {
	commands, home, shell, git := nativeCoreGitEnvironmentCommands(t)
	for _, fixture := range []struct {
		name string
		env  []string
		want string
	}{
		{"absent", nil, "true\n"},
		{"zero", []string{"GIT_CONFIG_COUNT=0"}, "true\n"},
		{"local", []string{"GIT_CONFIG_COUNT=4", "GIT_CONFIG_KEY_0=proof.entry", "GIT_CONFIG_VALUE_0=first", "GIT_CONFIG_KEY_1=core.longpaths", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=proof.entry", "GIT_CONFIG_VALUE_2=second", "GIT_CONFIG_KEY_3=proof.empty", "GIT_CONFIG_VALUE_3=", "GIT_CONFIG_PARAMETERS='proof.parameters=local'"}, "false\ntrue\n"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			fixture.env = append(fixture.env, "GIT_CONFIG_NOSYSTEM=1")
			for _, executable := range []string{git, shell} {
				t.Run(filepath.Base(executable), func(t *testing.T) {
					type configCheck struct {
						key  string
						want string
						exit int
					}
					checks := []configCheck{{"core.longpaths", fixture.want, 0}, {"proof.ambient", "", 1}, {"proof.parameters", "", 1}}
					if fixture.name == "local" {
						checks[2] = configCheck{"proof.parameters", "local\n", 0}
						checks = append(checks, configCheck{"proof.entry", "first\nsecond\n", 0}, configCheck{"proof.empty", "\n", 0})
					}
					for _, check := range checks {
						args := []string{"config", "--get-all", check.key}
						if executable == shell {
							args = append([]string{"-c", `git "$@"`, "sh"}, args...)
						}
						before := slices.Clone(fixture.env)
						result, err := commands.Run(context.Background(), publication.Command{Executable: executable, Args: args, Directory: home, Environment: fixture.env})
						if err != nil || result.ExitCode != check.exit || strings.ReplaceAll(string(result.Stdout), "\r\n", "\n") != check.want || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
							t.Fatalf("config %s: exit=%d error=%v stdout=%q stderr=%q", check.key, result.ExitCode, err, result.Stdout, result.Stderr)
						}
						if !slices.Equal(before, fixture.env) {
							t.Fatal("command environment was mutated")
						}
					}
				})
			}
		})
	}
}

func TestNativeCoreGitEnvironmentDirectoryAndIdentity(t *testing.T) {
	commands, home, shell, git := nativeCoreGitEnvironmentCommands(t)
	root := filepath.Join(home, "repo")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := commands.Run(context.Background(), publication.Command{Executable: git, Directory: root, Args: []string{"init", "--template="}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("init: exit=%d error=%v", result.ExitCode, err)
	}
	env := []string{"GIT_CONFIG_COUNT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_DIR=" + filepath.Join(root, ".git"), "GIT_WORK_TREE=" + root, "HOME=" + filepath.Join(home, "foreign-home"), "CODEX_HOME=foreign", "OPENAI_API_KEY=synthetic-must-not-cross", "ANTHROPIC_API_KEY=synthetic-must-not-cross"}
	for _, executable := range []string{git, shell} {
		args := []string{"rev-parse", "--show-toplevel"}
		if executable == shell {
			args = []string{"-c", `test "$HOME" -ef "$1" && test "$CODEX_HOME" != foreign && test -z "$OPENAI_API_KEY$ANTHROPIC_API_KEY" && git rev-parse --show-toplevel`, "sh", home}
		}
		result, err := commands.Run(context.Background(), publication.Command{Executable: executable, Directory: home, Args: args, Environment: env})
		if err != nil || result.ExitCode != 0 || !strings.EqualFold(strings.TrimSpace(string(result.Stdout)), filepath.ToSlash(root)) || len(result.Stderr) != 0 {
			t.Fatalf("directory/identity: exit=%d error=%v", result.ExitCode, err)
		}
	}
}

func TestNativeCoreGitEnvironmentInvalidBeforeLaunch(t *testing.T) {
	commands, home, shell, _ := nativeCoreGitEnvironmentCommands(t)
	for _, fixture := range []struct {
		name string
		env  []string
	}{
		{"empty-count", []string{"GIT_CONFIG_COUNT="}},
		{"negative-count", []string{"GIT_CONFIG_COUNT=-1"}},
		{"malformed-count", []string{"GIT_CONFIG_COUNT=invalid"}},
		{"overflow-count", []string{"GIT_CONFIG_COUNT=999999999999999999999999999"}},
		{"above-limit", []string{"GIT_CONFIG_COUNT=257"}},
		{"missing-key", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_VALUE_0=local"}},
		{"missing-value", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=proof.local"}},
		{"invalid-entry", []string{"GIT_CONFIG_COUNT=0", "invalid"}},
		{"nul-entry", []string{"GIT_CONFIG_COUNT=0", "GIT_DIR=invalid\x00"}},
		{"no-room", nativeCoreGitEnvironmentPairs(256)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			marker := filepath.Join(home, "launched-"+fixture.name)
			result, err := commands.Run(context.Background(), publication.Command{Executable: shell, Directory: home, Args: []string{"-c", `printf launched > "$1"`, "sh", marker}, Environment: fixture.env})
			if err == nil || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
				t.Errorf("invalid configuration must fail before launch: exit=%d error=%v", result.ExitCode, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("invalid configuration launched the shell: marker error=%v", err)
			}
		})
	}
	result, err := commands.Run(context.Background(), publication.Command{Executable: shell, Directory: home, Args: []string{"-c", "git config --get core.longpaths"}, Environment: nativeCoreGitEnvironmentPairs(255)})
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(string(result.Stdout)) != "true" || len(result.Stderr) != 0 {
		t.Fatalf("last available config slot: exit=%d error=%v stdout=%q stderr=%q", result.ExitCode, err, result.Stdout, result.Stderr)
	}
}

func nativeCoreGitEnvironmentPairs(count int) []string {
	env := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", count), "GIT_CONFIG_NOSYSTEM=1"}
	for i := 0; i < count; i++ {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=proof.entry", i), fmt.Sprintf("GIT_CONFIG_VALUE_%d=local", i))
	}
	return env
}

func TestNativeCoreGitEnvironmentDirectPrecedence(t *testing.T) {
	commands, home, shell, git := nativeCoreGitEnvironmentCommands(t)
	for _, fixture := range []struct {
		name       string
		executable string
		env        []string
		options    []string
		want       string
		all        string
	}{
		{"direct-parameters", git, []string{"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS='core.longpaths=false'"}, nil, "true\n", "false\ntrue\n"},
		{"direct-order", git, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.longpaths", "GIT_CONFIG_VALUE_0=false", "GIT_CONFIG_PARAMETERS='core.longpaths=false'"}, nil, "true\n", "false\nfalse\ntrue\n"},
		{"direct-argv", git, []string{"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS='core.longpaths=false'"}, []string{"-c", "core.longpaths=false"}, "false\n", "false\ntrue\nfalse\n"},
		{"direct-full-count", git, nativeCoreGitEnvironmentPairs(256), nil, "true\n", "true\n"},
		{"shell-parameters", shell, []string{"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS='core.longpaths=false'"}, nil, "false\n", "true\nfalse\n"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, query := range []struct {
				option string
				want   string
			}{{"--get", fixture.want}, {"--get-all", fixture.all}} {
				args := append(slices.Clone(fixture.options), "config", query.option, "core.longpaths")
				if fixture.executable == shell {
					args = append([]string{"-c", `git "$@"`, "sh"}, args...)
				}
				beforeArgs, beforeEnv := slices.Clone(args), slices.Clone(fixture.env)
				result, err := commands.Run(context.Background(), publication.Command{Executable: fixture.executable, Directory: home, Args: args, Environment: fixture.env})
				if err != nil || result.ExitCode != 0 || strings.ReplaceAll(string(result.Stdout), "\r\n", "\n") != query.want || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
					t.Errorf("config %s core.longpaths: exit=%d error=%v stdout=%q stderr=%q want=%q", query.option, result.ExitCode, err, result.Stdout, result.Stderr, query.want)
				}
				if !slices.Equal(beforeArgs, args) || !slices.Equal(beforeEnv, fixture.env) {
					t.Fatal("command arguments or environment were mutated")
				}
			}
		})
	}
}

func TestNativeCoreGitEnvironmentDirectInvalidBeforeLaunch(t *testing.T) {
	commands, home, _, git := nativeCoreGitEnvironmentCommands(t)
	for _, fixture := range []struct {
		name string
		env  []string
	}{
		{"empty-count", []string{"GIT_CONFIG_COUNT="}},
		{"negative-count", []string{"GIT_CONFIG_COUNT=-1"}},
		{"malformed-count", []string{"GIT_CONFIG_COUNT=invalid"}},
		{"overflow-count", []string{"GIT_CONFIG_COUNT=999999999999999999999999999"}},
		{"above-limit", []string{"GIT_CONFIG_COUNT=257"}},
		{"missing-key", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_VALUE_0=local"}},
		{"missing-value", []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=proof.local"}},
		{"invalid-entry", []string{"GIT_CONFIG_COUNT=0", "invalid"}},
		{"nul-entry", []string{"GIT_CONFIG_COUNT=0", "GIT_DIR=invalid\x00"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			marker := filepath.Join(home, "direct-launched-"+fixture.name)
			result, err := commands.Run(context.Background(), publication.Command{Executable: git, Directory: home, Args: []string{"config", "--file", marker, "proof.launched", "true"}, Environment: fixture.env})
			if err == nil || result.ExitCode != -1 || len(result.Stdout) != 0 || len(result.Stderr) != 0 {
				t.Errorf("invalid configuration must fail before launch: exit=%d error=%v", result.ExitCode, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("invalid configuration launched Git: marker error=%v", err)
			}
		})
	}
}
