package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func credentialCleanupLimitedProcess(t *testing.T, name string) bool {
	t.Helper()
	before, backup, restore := credentialCleanupTokenPrivileges(t)
	if !backup && !restore {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY, &source); err != nil {
		t.Fatal("open owned source token", err)
	}
	defer func() {
		if err := source.Close(); err != nil {
			t.Error("owned source token close", err)
		}
	}()
	limited := new(windows.Token)
	var pin runtime.Pinner
	pin.Pin(limited)
	defer pin.Unpin()
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	const disableMaximumPrivileges = 1
	ok, _, callErr := create.Call(uintptr(source), disableMaximumPrivileges, 0, 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(limited)))
	if ok == 0 || *limited == 0 {
		t.Fatal("create owned limited token", callErr)
	}
	defer func() {
		if err := limited.Close(); err != nil {
			t.Error("owned limited token close", err)
		}
	}()
	limitedUser, err := limited.GetTokenUser()
	if err != nil || !windows.EqualSid(user.User.Sid, limitedUser.User.Sid) {
		t.Fatal("owned limited token changed USER SID", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^"+regexp.QuoteMeta(name)+"$", "-test.count=1", "-test.timeout=120s", "-test.v")
	cmd.Env = append(os.Environ(), "BATUTA_CLEANUP_PARENT_USER_SID="+user.User.Sid.String())
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(*limited)}
	output, runErr := cmd.CombinedOutput()
	pid, exited, success := 0, false, false
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	if cmd.ProcessState != nil {
		exited, success = cmd.ProcessState.Exited(), cmd.ProcessState.Success()
	}
	t.Logf("actual limited cleanup child test=%s pid=%d exited=%t success=%t err=%v output:\n%s", name, pid, exited, success, runErr, output)
	if runErr != nil || ctx.Err() != nil || !exited || !success {
		t.Fatal("owned limited cleanup child failed", runErr, ctx.Err())
	}
	after, afterBackup, afterRestore := credentialCleanupTokenPrivileges(t)
	afterUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !windows.EqualSid(user.User.Sid, afterUser.User.Sid) || !bytes.Equal(before, after) || backup != afterBackup || restore != afterRestore {
		t.Fatal("owned child launch changed parent token", err)
	}
	t.Logf("actual limited cleanup parent USER=%s privilegesUnchanged=true childExited=true", user.User.Sid.String())
	return true
}
