package service

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
)

type nativeAccountRunner struct {
	accounts nativeAccounts
	other    publication.CommandRunner
}

func (r nativeAccountRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	for _, account := range r.accounts.byCLI {
		if command.Executable == account.executable {
			if err := account.authorizeCommand(command); err != nil {
				return publication.CommandResult{ExitCode: -1}, err
			}
			return (session.NativeRunner{Manager: r.accounts.manager, Environment: account.env}).Run(ctx, command)
		}
	}
	if cli := filepath.Base(command.Executable); cli == "codex" || cli == "claude" {
		return publication.CommandResult{ExitCode: -1}, fmt.Errorf("%w: unbound native CLI", domain.ErrInvalidReference)
	}
	return r.other.Run(ctx, command)
}

// Core builds each invocation itself. Match against that same public adapter
// contract so account selection cannot change the model, effort or CLI flags.
func (a nativeAccount) authorizeCommand(command publication.Command) error {
	return a.authorizeNativeCommand(command, false)
}

func (a nativeAccount) authorizeReadonlyCommand(command publication.Command) error {
	return a.authorizeNativeCommand(command, true)
}

func (a nativeAccount) authorizeNativeCommand(command publication.Command, readonly bool) error {
	if command.Executable != a.executable || !filepath.IsAbs(command.Directory) {
		return domain.ErrInvalidReference
	}
	const payload = "__console_native_payload__"
	for _, member := range a.members {
		efforts := []string{member.Effort}
		if member.Effort == "" {
			efforts = []string{"", "low", "medium", "high", "xhigh"}
		}
		for _, effort := range efforts {
			request := executor.Request{Model: member.Model, Effort: effort, Cwd: command.Directory, Brief: payload, Prompt: payload, BriefFile: payload}
			kinds := []string{"run", "file", "readonly"}
			if readonly {
				kinds = []string{"readonly"}
			}
			for _, kind := range kinds {
				build := a.adapter.Command
				if kind == "readonly" {
					build = a.adapter.ReadonlyCommand
				} else if kind == "file" {
					if a.adapter.RunFile == "" {
						continue
					}
					adapter := a.adapter
					adapter.Run, adapter.RunFile = adapter.RunFile, ""
					build = adapter.Command
				}
				allowed, err := build(request)
				if err != nil || len(allowed.Args) != len(command.Args) {
					continue
				}
				changed := request
				changed.Model = "__console_unbound_model__"
				foreign, err := build(changed)
				if err != nil || slices.Equal(allowed.Args, foreign.Args) {
					continue
				}
				if member.Effort != "" {
					changed = request
					changed.Effort = "__console_unbound_effort__"
					foreign, err = build(changed)
					if err != nil || slices.Equal(allowed.Args, foreign.Args) {
						continue
					}
				}
				matched := true
				for i, token := range allowed.Args {
					pattern := "\\A" + strings.ReplaceAll(regexp.QuoteMeta(token), payload, "(?s:.+)") + "\\z"
					if !regexp.MustCompile(pattern).MatchString(command.Args[i]) || strings.ContainsRune(command.Args[i], '\x00') {
						matched = false
						break
					}
				}
				if matched {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("%w: native invocation is outside the selected model/effort contract", domain.ErrInvalidReference)
}
