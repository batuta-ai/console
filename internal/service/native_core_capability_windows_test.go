package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/publication"
)

const task55CapabilityPrompt = "Run the shell command `git rev-parse HEAD` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command's output, and stop."

func task55CoreCapabilityPE() (bool, int) {
	if len(os.Args) != 8 && len(os.Args) != 10 {
		return false, 0
	}
	if os.Args[1] != "-test.run=^TestTask43WindowsNativeCoreProviderFixture$" || os.Args[2] != "--" || os.Args[3] != "task43-core-provider" || os.Args[4] != "run" || os.Args[5] != "--model" || os.Args[6] != "qa-native" {
		return false, 0
	}
	if os.Args[len(os.Args)-1] != task55CapabilityPrompt {
		return false, 0
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true, 90
	}
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "native-fixture-mode"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "capability fixture mode:", err)
		return true, 1
	}
	if string(mode) == "effort" {
		if len(os.Args) != 10 || os.Args[7] != "--effort" || os.Args[8] != "low" {
			return false, 0
		}
	} else if len(os.Args) != 8 {
		return false, 0
	}
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Stderr = os.Stderr
	out, err := command.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "capability git rev-parse HEAD:", err)
		return true, 1
	}
	fmt.Printf("BATUTA-CAPABLE %s\n", strings.TrimSpace(string(out)))
	return true, 0
}

func task55CapabilityArgs(effort bool) []string {
	args := []string{"-test.run=^TestTask43WindowsNativeCoreProviderFixture$", "--", "task43-core-provider", "run", "--model", "qa-native"}
	if effort {
		args = append(args, "--effort", "low")
	}
	return append(args, task55CapabilityPrompt)
}

func task55CapabilityImage(t *testing.T) string {
	t.Helper()
	_, tools := task43CoreHome(t)
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(tools, "codex.exe")
	if err := os.WriteFile(cli, data, 0600); err != nil {
		t.Fatal(err)
	}
	return cli
}

func task55CapabilityEnvironment(t *testing.T) ([]string, string) {
	t.Helper()
	env, home := task43CoreHome(t)
	env = accounts.RuntimeEnvironment(os.Environ(), env)
	config := filepath.Join(home, "gitconfig-empty")
	task43CoreWriteFile(t, config, "")
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+config, "GIT_OPTIONAL_LOCKS=0"), home
}

func task55CapabilityGit(t *testing.T, env []string, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir, command.Env = root, env
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("owned capability Git %v: %v %s", args, err, out)
	}
	return string(out)
}

func task55CapabilityRepository(t *testing.T, env []string) string {
	t.Helper()
	_, root := task43CoreHome(t)
	task55CapabilityGit(t, env, root, "init", "-q", "-b", "main")
	task43CoreWriteFile(t, filepath.Join(root, "out.txt"), "original tracked output\n")
	task55CapabilityGit(t, env, root, "add", "out.txt")
	task55CapabilityCommit(t, env, root, "first")
	return root
}

func task55CapabilityCommit(t *testing.T, env []string, root, message string) {
	t.Helper()
	hooks := filepath.Join(t.TempDir(), "empty-hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	task55CapabilityGit(t, env, root, "-c", "user.name=capability-fixture", "-c", "user.email=qa@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+hooks, "commit", "-q", "--allow-empty", "-m", message)
}

func task55CapabilityFiles(t *testing.T, roots ...string) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err == nil {
				files[path] = sha256.Sum256(data)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestTask55WindowsNativeCoreCapabilityProtocol(t *testing.T) {
	cli := task55CapabilityImage(t)
	for _, mode := range []string{"question", "valid", "fix", "invalid", "hold", "review-hold", "review_tree", "review_index", "preparation", "capacity", "effort"} {
		t.Run(mode, func(t *testing.T) {
			env, home := task55CapabilityEnvironment(t)
			root := task55CapabilityRepository(t, env)
			if mode == "capacity" {
				task43CoreWriteFile(t, filepath.Join(home, "native-success-gate"), "hold")
			} else if mode != "question" {
				task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), mode)
			}
			manager := session.New(home)
			t.Cleanup(func() {
				if err := manager.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			previous := ""
			for _, revision := range []string{"first", "second"} {
				if revision == "second" {
					task55CapabilityCommit(t, env, root, revision)
				}
				head := strings.TrimSpace(task55CapabilityGit(t, env, root, "rev-parse", "HEAD"))
				if head == previous {
					t.Fatal("owned HEAD did not change")
				}
				previous = head
				status := task55CapabilityGit(t, env, root, "status", "--porcelain")
				before := task55CapabilityFiles(t, root, home)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				result, err := (session.NativeRunner{Manager: manager, Environment: env}).Run(ctx, publication.Command{
					Executable: cli, Args: task55CapabilityArgs(mode == "effort"), Directory: root, Environment: env,
				})
				cancel()
				if err != nil || result.ExitCode != 0 || string(result.Stdout) != "BATUTA-CAPABLE "+head+"\n" || len(result.Stderr) != 0 {
					t.Fatalf("effect-free actual PE probe: result=%+v err=%v", result, err)
				}
				if after := task55CapabilityFiles(t, root, home); !reflect.DeepEqual(before, after) {
					t.Fatal("probe changed repository/index or HOME task/log/hold files")
				}
				if got := task55CapabilityGit(t, env, root, "status", "--porcelain"); got != status {
					t.Fatalf("probe changed porcelain: before=%q after=%q", status, got)
				}
				if got := strings.TrimSpace(task55CapabilityGit(t, env, root, "rev-parse", "HEAD")); got != head {
					t.Fatalf("probe changed HEAD: %s -> %s", head, got)
				}
			}
		})
	}
}

func TestTask55WindowsNativeCoreCapabilityRejectsInvalidInvocation(t *testing.T) {
	cli := task55CapabilityImage(t)
	for _, mode := range []string{"question", "valid", "effort", "review_tree", "review_index"} {
		t.Run(mode, func(t *testing.T) {
			valid := task55CapabilityArgs(mode == "effort")
			cases := map[string][]string{
				"missing_prompt": valid[:len(valid)-1],
				"extra_argument": append(append([]string{}, valid...), "extra"),
				"missing_model":  append(append([]string{}, valid[:4]...), valid[6:]...),
			}
			for name, change := range map[string]func([]string){
				"substring_prefix": func(a []string) { a[len(a)-1] = "prefix " + task55CapabilityPrompt },
				"substring_suffix": func(a []string) { a[len(a)-1] += " suffix" },
				"model_prompt":     func(a []string) { a[5], a[len(a)-1] = task55CapabilityPrompt, "ordinary task" },
				"wrong_model":      func(a []string) { a[5] = "unbound" },
				"misordered_model": func(a []string) { a[4], a[5] = a[5], a[4] },
				"wrong_separator":  func(a []string) { a[1] = "wrong" },
				"wrong_provider":   func(a []string) { a[2] = "wrong" },
				"review_contract":  func(a []string) { a[3] = "review" },
			} {
				args := append([]string{}, valid...)
				change(args)
				cases[name] = args
			}
			if mode == "effort" {
				for name, effort := range map[string]string{"wrong_effort": "high", "empty_effort": "", "effort_prompt": task55CapabilityPrompt} {
					args := append([]string{}, valid...)
					args[7] = effort
					cases[name] = args
				}
				cases["missing_effort"] = task55CapabilityArgs(false)
				cases["missing_effort_value"] = append(append([]string{}, valid[:7]...), task55CapabilityPrompt)
			} else {
				cases["unexpected_effort"] = task55CapabilityArgs(true)
			}
			for name, args := range cases {
				t.Run(name, func(t *testing.T) {
					env, home := task55CapabilityEnvironment(t)
					root := task55CapabilityRepository(t, env)
					if mode != "question" {
						task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), mode)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, cli, args...)
					command.Dir, command.Env = root, env
					out, err := command.CombinedOutput()
					if command.ProcessState == nil {
						t.Fatalf("actual PE did not execute: %v", err)
					}
					if ctx.Err() != nil || bytes.Contains(out, []byte("BATUTA-CAPABLE ")) {
						t.Fatalf("invalid invocation gained capability or hung: %v %s", err, out)
					}
				})
			}
		})
	}
}

func TestTask55WindowsNativeCoreCapabilityGitAndCredentialFailures(t *testing.T) {
	cli := task55CapabilityImage(t)
	for _, name := range []string{"no_repository", "unborn_head", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			env, home := task55CapabilityEnvironment(t)
			_, root := task43CoreHome(t)
			if name == "unborn_head" {
				task55CapabilityGit(t, env, root, "init", "-q", "-b", "main")
			} else if strings.HasSuffix(name, "API_KEY") {
				root = task55CapabilityRepository(t, env)
				env = append(env, name+"=synthetic-contamination")
			}
			before := task55CapabilityFiles(t, root, home)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, cli, task55CapabilityArgs(false)...)
			command.Dir, command.Env = root, env
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || ctx.Err() != nil || stdout.Len() != 0 {
				t.Fatalf("failed probe must exit nonzero without success: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if strings.HasSuffix(name, "API_KEY") {
				if exit.ExitCode() != 90 {
					t.Fatalf("credential boundary exit=%d, want 90", exit.ExitCode())
				}
			} else if stderr.Len() == 0 {
				t.Fatal("Git failure was not visible on stderr")
			}
			if after := task55CapabilityFiles(t, root, home); !reflect.DeepEqual(before, after) {
				t.Fatal("failed probe produced task side effects")
			}
		})
	}
}
