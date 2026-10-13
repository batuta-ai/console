package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNativeCleanupLimitedChildRejectsMissingTarget(t *testing.T) {
	credentialCleanupRejectedChild(t, "missing-target", "owned limited cleanup child did not complete target test")
}

func TestNativeCleanupLimitedChildRejectsPrivilegedMarker(t *testing.T) {
	credentialCleanupRejectedChild(t, "privileged-marker", "cleanup child marker has enabled backup/restore privileges")
}

func credentialCleanupRejectedChild(t *testing.T, scenario, want string) {
	t.Helper()
	before, backup, restore := credentialCleanupTokenPrivileges(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("BATUTA_CLEANUP_REJECTION_CASE") == scenario {
		if scenario == "privileged-marker" && !backup && !restore {
			t.Log("actual old helper recursively reached a limited child despite a privileged marker")
			return
		}
		if !backup && !restore {
			t.Fatal("controlled rejection process does not have enabled backup/restore privileges")
		}
		target := t.Name()
		if scenario == "missing-target" {
			target = "TestBatutaCleanupMissingTargetMustNeverExist"
		} else if os.Getenv("BATUTA_CLEANUP_PARENT_USER_SID") != user.User.Sid.String() {
			t.Fatal("controlled privileged marker does not match actual current USER")
		}
		if !credentialCleanupLimitedProcess(t, target) {
			t.Fatal("controlled privileged helper did not attempt validation")
		}
		t.Logf("unsafe cleanup helper accepted real %s process", scenario)
		return
	}
	if backup == false && restore == false {
		t.Fatal("rejection qualification requires an actual privileged parent")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.timeout=120s", "-test.v")
	cmd.WaitDelay = 5 * time.Second
	for _, value := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(value), "BATUTA_CLEANUP_REJECTION_CASE=") || strings.HasPrefix(strings.ToUpper(value), "BATUTA_CLEANUP_PARENT_USER_SID=") {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "BATUTA_CLEANUP_REJECTION_CASE="+scenario)
	if scenario == "privileged-marker" {
		cmd.Env = append(cmd.Env, "BATUTA_CLEANUP_PARENT_USER_SID="+user.User.Sid.String())
	}
	output, runErr := cmd.CombinedOutput()
	t.Logf("actual cleanup rejection scenario=%s target=%s err=%v output:\n%s", scenario, t.Name(), runErr, output)
	after, afterBackup, afterRestore := credentialCleanupTokenPrivileges(t)
	afterUser, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if userErr != nil || !windows.EqualSid(user.User.Sid, afterUser.User.Sid) || !bytes.Equal(before, after) || backup != afterBackup || restore != afterRestore {
		t.Fatal("controlled rejection child changed parent USER/privileges", userErr)
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || ctx.Err() != nil || cmd.ProcessState == nil || !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != 1 {
		t.Fatal("unsafe cleanup helper accepted real rejected child", scenario, runErr, ctx.Err())
	}
	text := string(output)
	failedTarget := regexp.MustCompile(`(?m)^--- FAIL: ` + regexp.QuoteMeta(t.Name()) + ` \([0-9.]+s\)\r?$`).Match(output)
	if !failedTarget || !strings.Contains(text, want) || strings.Contains(text, "--- SKIP:") || strings.Contains(text, "no tests to run") && scenario != "missing-target" {
		t.Fatal("controlled child failed without the required validation cause", scenario, want)
	}
}
