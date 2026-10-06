package service

import (
	"path/filepath"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/executor"
	"github.com/batuta-ai/core/publication"
)

func TestNativeInvocationPolicyUsesPublicAdapterAndExactModelEffort(t *testing.T) {
	adapter := executor.Adapter{Name: "codex", Run: `codex exec {model_flags} {cwd_flag} "{brief}"`, RunFile: `codex exec {model_flags} {cwd_flag} --file "{brief_file}"`, Readonly: `codex exec --readonly {model_flags} {cwd_flag} "{prompt}"`, ModelFlags: `--model {model} -c model_reasoning_effort="{effort}"`, CwdFlag: `--cd {cwd}`, BriefLimitLines: 1}
	account := nativeAccount{executable: "/selected/codex", adapter: adapter, members: []domain.Member{{Model: "approved", Effort: "high"}}}
	request := executor.Request{Cwd: t.TempDir(), Brief: "authorized task\nsecond line", BriefFile: filepath.Join(t.TempDir(), "brief.md"), Prompt: "verify this diff", Model: "approved", Effort: "high"}
	for _, readonly := range []bool{false, true} {
		build := adapter.Command
		if readonly {
			build = adapter.ReadonlyCommand
		}
		invocation, err := build(request)
		if err != nil {
			t.Fatal(err)
		}
		command := publication.Command{Executable: account.executable, Directory: invocation.Dir, Args: invocation.Args}
		if err := account.authorizeCommand(command); err != nil {
			t.Fatal("public invocation rejected", readonly, err)
		}
		for _, changed := range []executor.Request{
			{Cwd: request.Cwd, Brief: "task", Prompt: "review", Model: "foreign", Effort: "high"},
			{Cwd: request.Cwd, Brief: "task", Prompt: "review", Model: "approved", Effort: "low"},
		} {
			bad, err := build(changed)
			if err != nil {
				t.Fatal(err)
			}
			command.Args = bad.Args
			if err := account.authorizeCommand(command); err == nil {
				t.Fatal("unbound model/effort submitted", readonly, changed)
			}
		}
	}
	account.adapter.ModelFlags = ""
	account.adapter.Run = `codex "{brief}"`
	if err := account.authorizeCommand(publication.Command{Executable: account.executable, Directory: request.Cwd, Args: []string{"task"}}); err == nil {
		t.Fatal("adapter with implicit model allowed")
	}
}
