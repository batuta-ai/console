package service

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
)

func TestWindowsNativeCoreEarlyFailureKeepsCause(t *testing.T) {
	svc, approval, skills, env, values := task43CoreApprovedFixture(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	svc.NativeExecutorExecutable = task43CoreParentImage(t, "", home, "pre_hello_exit")
	state, err, active := task43CoreParentRun(t, svc, approval, skills, env, values, "")
	if !errors.Is(err, domain.ErrExecutionUnavailable) || !strings.Contains(err.Error(), "owned startup diagnostic") || state != "" || active {
		t.Fatalf("early child failure lost unavailable identity/diagnostic: state=%q active=%v error=%v", state, active, err)
	}
}

func TestWindowsNativeCoreMissingShellKeepsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	path, err := nativeCoreShellPath()
	if path != "" || !errors.Is(err, domain.ErrExecutionUnavailable) {
		t.Fatal("missing real shell/Git lost unavailable cause", path, err)
	}
}

func TestWindowsNativeCoreParentRejectsUnqualifiedHello(t *testing.T) {
	svc, approval, skills, env, values := task43CoreApprovedFixture(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	svc.NativeExecutorExecutable = task43CoreParentImage(t, "", home, "unqualified_hello")
	state, err, active := task43CoreParentRun(t, svc, approval, skills, env, values, "")
	if state != "" || active || !errors.Is(err, domain.ErrExecutionUnavailable) {
		t.Fatal("unqualified child hello was accepted", state, active, err)
	}
	if _, err := os.Stat(home + string(os.PathSeparator) + "native-launches"); !os.IsNotExist(err) {
		t.Fatal("unqualified helper submitted worker", err)
	}
}

func TestWindowsNativeCoreParentRejectsUnboundArgv(t *testing.T) {
	svc, approval, skills, env, values := task43CoreApprovedFixture(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	svc.NativeExecutorExecutable = task43CoreParentImage(t, values["codex"].Executable, home, "foreign_argv")
	_, err, _ := task43CoreParentRun(t, svc, approval, skills, env, values, "")
	if !errors.Is(err, domain.ErrInvalidReference) {
		t.Fatal("unbound selected-model argv was accepted", err)
	}
	if _, err := os.Stat(home + string(os.PathSeparator) + "native-launches"); !os.IsNotExist(err) {
		t.Fatal("unbound argv submitted worker", err)
	}
}
