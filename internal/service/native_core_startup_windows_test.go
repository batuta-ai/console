package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestWindowsNativeCoreEarlyDiagnosticKeepsUTF8Bound(t *testing.T) {
	svc, approval, skills, env, values := task43CoreApprovedFixture(t)
	home := accounts.ExplicitHome(values["codex"].Environment)
	svc.NativeExecutorExecutable = task43CoreParentImage(t, "", home, "long_early_diagnostic")
	_, err, active := task43CoreParentRun(t, svc, approval, skills, env, values, "")
	if !errors.Is(err, domain.ErrExecutionUnavailable) || active || !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), strings.Repeat("x", 4095)) || strings.Contains(err.Error(), "DIAGNOSTIC-END") {
		t.Fatal("early diagnostic lost its valid bounded prefix", err)
	}
}

func TestWindowsNativeCoreHandshakeCancellationKeepsCause(t *testing.T) {
	for _, action := range []string{"cancel", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			svc, approval, skills, _, values := task43CoreApprovedFixture(t)
			home := accounts.ExplicitHome(values["codex"].Environment)
			svc.NativeExecutorExecutable = task43CoreParentImage(t, "", home, "pre_hello_hold")
			job, finished, ctx := task43CorePreparedRun(t, svc, approval, values, skills)
			for {
				if _, err := os.Stat(filepath.Join(home, "native-parent-home")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			if action == "cancel" {
				job.cancel()
			} else if err := svc.Session.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case outcome := <-job.ready:
				if !errors.Is(outcome.err, context.Canceled) || errors.Is(outcome.err, domain.ErrExecutionUnavailable) {
					t.Fatal("handshake cancellation became unavailable", outcome.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
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
	if _, err := os.Stat(home + string(os.PathSeparator) + "unqualified-start"); !os.IsNotExist(err) {
		t.Fatal("unqualified helper received start authority", err)
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
