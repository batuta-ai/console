package adapters

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func commandAdapter(t *testing.T, id string) Adapter {
	t.Helper()
	ad, err := Get(id)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ad.CLI), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return ad
}

func TestCommandContract(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			ad := commandAdapter(t, id)
			path := filepath.Join(os.Getenv("PATH"), id)
			model := `future-model;$(literal)"quote`
			for _, tc := range []struct {
				name string
				in   CommandRequest
				args []string
			}{
				{"interactive", CommandRequest{Mode: "interactive", Prompt: "  literal goal\nline 2 ", Model: model, Effort: "high"}, []string{"--model", model}},
				{"defaults", CommandRequest{Mode: "interactive"}, nil},
				{"whitespace", CommandRequest{Mode: "interactive", Prompt: " \t\n", Model: " \t", Effort: " \n"}, nil},
				{"init", CommandRequest{Mode: "init", Prompt: "init goal"}, []string{"init goal"}},
				{"login", CommandRequest{Mode: "login"}, []string{"login"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					want := append([]string{path}, tc.args...)
					if tc.name == "interactive" {
						if id == "codex" {
							want = append(want, "--config", `model_reasoning_effort="high"`)
						} else {
							want = append(want, "--effort", "high")
						}
						want = append(want, tc.in.Prompt)
					}
					if tc.name == "login" && id == "claude" {
						want = []string{path, "auth", "login"}
					}
					command, err := ad.Command(tc.in)
					if err != nil || !slices.Equal(command.Argv, want) {
						t.Fatalf("argv=%q want=%q err=%v", command.Argv, want, err)
					}
				})
			}
			levels := []string{"low", "medium", "high", "xhigh", "max"}
			if id == "codex" {
				levels = append(levels, "ultra")
			}
			for _, level := range levels {
				command, err := ad.Command(CommandRequest{Mode: "interactive", Effort: level})
				want := []string{path, "--effort", level}
				if id == "codex" {
					want = []string{path, "--config", `model_reasoning_effort="` + level + `"`}
				}
				if err != nil || !slices.Equal(command.Argv, want) {
					t.Errorf("effort %q: %q %v", level, command.Argv, err)
				}
			}
		})
	}
}

func TestCommandRejectsInvalidConfiguration(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			ad := commandAdapter(t, id)
			for _, effort := range []string{"default", "none", "minimal", "HIGH", " high ", "ultracode", "high\x00", "high\nother=true", `high",sandbox_mode="danger-full-access`} {
				_, err := ad.Command(CommandRequest{Mode: "interactive", Effort: effort})
				if !errors.Is(err, ErrInvalidCommand) {
					t.Errorf("effort %q: %v", effort, err)
				}
			}
			if id == "claude" {
				if _, err := ad.Command(CommandRequest{Mode: "interactive", Effort: "ultra"}); !errors.Is(err, ErrInvalidCommand) {
					t.Errorf("claude ultra: %v", err)
				}
			}
			for _, in := range []CommandRequest{
				{Mode: "interactive", Prompt: "--help"}, {Mode: "interactive", Prompt: " \t--model=other"}, {Mode: "interactive", Prompt: "a\x00b"},
				{Mode: "init", Prompt: "--help"}, {Mode: "interactive", Model: "--help"}, {Mode: "interactive", Model: " \t--config=x"}, {Mode: "interactive", Model: "m\x00"},
				{Mode: "login", Prompt: "goal"}, {Mode: "login", Model: "model"}, {Mode: "login", Effort: "high"},
				{Mode: "init", Argv: []string{"/bin/sh"}},
				{Mode: "interactive", Argv: []string{""}}, {Mode: "interactive", Argv: []string{"/bin/sh", "a\x00"}},
				{Mode: "interactive", Argv: []string{"/bin/sh"}, Model: "configured"}, {Mode: "interactive", Argv: []string{"/bin/sh"}, Effort: "high"},
			} {
				if _, err := ad.Command(in); !errors.Is(err, ErrInvalidCommand) {
					t.Errorf("%+v: %v", in, err)
				}
			}
		})
	}
}

func TestUnsupportedCommandsAndLookupCause(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, id := range []string{"cursor-agent", "opencode", "gemini"} {
		ad, _ := Get(id)
		for _, mode := range []string{"interactive", "init", "login"} {
			for _, custom := range [][]string{nil, {"/bin/sh"}} {
				if _, err := ad.Command(CommandRequest{Mode: mode, Argv: custom}); !errors.Is(err, ErrUnsupportedCommand) {
					t.Errorf("%s/%s/%v: %v", id, mode, custom, err)
				}
			}
		}
	}
	for _, id := range []string{"codex", "claude"} {
		ad, _ := Get(id)
		for _, mode := range []string{"", "exec", "print", "custom", "unsupported"} {
			if _, err := ad.Command(CommandRequest{Mode: mode}); !errors.Is(err, ErrUnsupportedCommand) {
				t.Errorf("mode %q: %v", mode, err)
			}
		}
		for _, mode := range []string{"interactive", "init", "login"} {
			command, err := ad.Command(CommandRequest{Mode: mode})
			var lookup *exec.Error
			if len(command.Argv) != 0 || !errors.Is(err, exec.ErrNotFound) || !errors.As(err, &lookup) {
				t.Errorf("%s/%s missing lookup: %+v %v", id, mode, command, err)
			}
		}
		argv := []string{"/literal/custom", "--supplied-goal"}
		command, err := ad.Command(CommandRequest{Mode: "interactive", Argv: argv})
		if err != nil || !slices.Equal(command.Argv, argv) || command.Report.Mode != "custom" {
			t.Fatalf("custom: %+v %v", command, err)
		}
		argv[0] = "mutation"
		if command.Argv[0] != "/literal/custom" {
			t.Fatal("custom argv is aliased")
		}
	}
}

func TestCommandRejectsSubcommandAsGoal(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			ad := commandAdapter(t, id)
			goals := []string{"agents", "doctor", "mcp", "plugin", "update"}
			if id == "codex" {
				goals = append(goals, "login", "logout", "exec", "e", "a", "delete", "help")
			} else {
				goals = append(goals, "auth", "install", "setup-token", "upgrade", "plugins", "kill")
			}
			for _, goal := range goals {
				command, err := ad.Command(CommandRequest{Mode: "interactive", Prompt: goal})
				if !errors.Is(err, ErrInvalidCommand) || len(command.Argv) != 0 {
					t.Errorf("goal %q interpreted as subcommand: %q %v", goal, command.Argv, err)
				}
			}
		})
	}
}
