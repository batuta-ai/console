package adapters

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	ErrUnsupportedCommand = errors.New("unsupported adapter command")
	ErrInvalidCommand     = errors.New("invalid command configuration")
)

type CommandRequest struct {
	Mode   string
	Prompt string
	Model  string
	Effort string
	Argv   []string
}

type Command struct {
	Argv   []string
	Report CommandReport
}

// CommandReport records launch inputs, never the provider's effective runtime
// configuration. Session.Argv remains authoritative independently of event retention.
type CommandReport struct {
	Mode       string         `json:"mode"`
	GoalSource string         `json:"goal_source"`
	Model      CommandSetting `json:"model"`
	Effort     CommandSetting `json:"effort"`
}

type CommandSetting struct {
	Source string `json:"source"`
	Value  string `json:"value,omitempty"`
}

// Command supports interactive PTY syntax qualified against Codex 0.160.0 and
// Claude 2.1.286. Effort validation is syntactic; model compatibility is the CLI's
// responsibility. Model names deliberately do not use registry catalog metadata.
func (a Adapter) Command(in CommandRequest) (Command, error) {
	if a.ID != "codex" && a.ID != "claude" {
		return Command{}, fmt.Errorf("%w: %s %s", ErrUnsupportedCommand, a.ID, in.Mode)
	}
	switch in.Mode {
	case "interactive", "init", "login":
	default:
		return Command{}, fmt.Errorf("%w: %s %s", ErrUnsupportedCommand, a.ID, in.Mode)
	}
	model, effort, prompt := nonblank(in.Model), nonblank(in.Effort), nonblank(in.Prompt)
	if len(in.Argv) > 0 {
		if in.Mode != "interactive" || model != "" || effort != "" {
			return Command{}, fmt.Errorf("%w: custom argv requires interactive mode without configured model or effort", ErrInvalidCommand)
		}
		if strings.TrimSpace(in.Argv[0]) == "" {
			return Command{}, fmt.Errorf("%w: custom executable is empty", ErrInvalidCommand)
		}
		for _, arg := range in.Argv {
			if strings.ContainsRune(arg, 0) {
				return Command{}, fmt.Errorf("%w: custom argv contains NUL", ErrInvalidCommand)
			}
		}
		return Command{Argv: slices.Clone(in.Argv), Report: CommandReport{
			Mode: "custom", GoalSource: "custom_argv", Model: CommandSetting{Source: "unknown"}, Effort: CommandSetting{Source: "unknown"},
		}}, nil
	}
	if in.Mode == "login" && (model != "" || effort != "" || prompt != "") {
		return Command{}, fmt.Errorf("%w: login does not accept a goal, model or effort", ErrInvalidCommand)
	}
	for _, field := range []struct{ name, value string }{{"model", model}, {"goal", prompt}} {
		if strings.ContainsRune(field.value, 0) || strings.HasPrefix(strings.TrimSpace(field.value), "-") {
			return Command{}, fmt.Errorf("%w: %s contains NUL or starts with an option prefix", ErrInvalidCommand, field.name)
		}
	}
	if a.reservedPrompt(prompt) {
		return Command{}, fmt.Errorf("%w: goal matches a %s subcommand; use a descriptive goal", ErrInvalidCommand, a.ID)
	}
	if !a.validEffort(effort) {
		return Command{}, fmt.Errorf("%w: %s effort %q", ErrInvalidCommand, a.ID, effort)
	}
	path, err := a.Lookup()
	if err != nil {
		return Command{}, fmt.Errorf("lookup %s for %s: %w", a.CLI, in.Mode, err)
	}
	command := Command{Argv: []string{path}, Report: CommandReport{
		Mode: in.Mode, GoalSource: "none", Model: commandSetting(model), Effort: commandSetting(effort),
	}}
	if in.Mode == "login" {
		if a.ID == "claude" {
			command.Argv = append(command.Argv, "auth")
		}
		command.Argv = append(command.Argv, "login")
		return command, nil
	}
	if model != "" {
		command.Argv = append(command.Argv, "--model", model)
	}
	if effort != "" {
		if a.ID == "codex" {
			// The allowlist contains only ASCII words, safe inside this TOML string.
			command.Argv = append(command.Argv, "--config", `model_reasoning_effort="`+effort+`"`)
		} else {
			command.Argv = append(command.Argv, "--effort", effort)
		}
	}
	if prompt != "" {
		command.Argv = append(command.Argv, prompt)
		command.Report.GoalSource = "prompt"
	}
	return command, nil
}

func (a Adapter) validEffort(effort string) bool {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	case "ultra":
		return a.ID == "codex"
	default:
		return false
	}
}

func nonblank(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

func commandSetting(value string) CommandSetting {
	if value == "" {
		return CommandSetting{Source: "cli_default"}
	}
	return CommandSetting{Source: "explicit", Value: value}
}

// Exact command names and aliases from the qualified CLI help cannot safely be
// used as positional goals. Keep them as errors rather than dispatching a command.
func (a Adapter) reservedPrompt(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	if a.ID == "codex" {
		switch prompt {
		case "agents", "exec", "e", "review", "login", "logout", "mcp", "plugin",
			"app-server", "remote-control", "app", "completion", "update", "doctor",
			"sandbox", "debug", "apply", "a", "resume", "queue", "archive", "delete",
			"migrate-rollouts", "unarchive", "fork", "cloud", "exec-server", "features", "help":
			return true
		}
	} else {
		switch prompt {
		case "agents", "attach", "auth", "auto-mode", "doctor", "gateway", "import",
			"install", "logs", "mcp", "plugin", "plugins", "project", "respawn", "rm",
			"setup-token", "stop", "kill", "ultrareview", "update", "upgrade":
			return true
		}
	}
	return false
}
