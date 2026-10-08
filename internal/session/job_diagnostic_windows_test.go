package session

import (
	"os"
	"os/exec"
	"testing"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsJobRootExitDiagnosis(t *testing.T) {
	home := task43SessionHome(t)
	env, privateHome, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(privateHome)
	argv := task43ProbeArgv(t, "descendant")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = accounts.RuntimeEnvironment(os.Environ(), env), home
	output := &probeOutput{overflow: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = probeWriter{output: output, stdout: true}, probeWriter{output: output, stdout: true}
	owned, err := startOwnedCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	status, observeErr := windows.WaitForSingleObject(owned.root, 1000)
	var accounting windowsJobAccounting
	queryErr := windows.QueryInformationJobObject(owned.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	output.mu.Lock()
	t.Logf("root PID=%d wait=%d error=%v active=%d query=%v output=%q", cmd.Process.Pid, status, observeErr, accounting.ActiveProcesses, queryErr, output.stdout)
	output.mu.Unlock()
	stopErr := owned.stop()
	cleanupErr, waitErr := waitOwnedOutcome(cmd, owned)
	output.mu.Lock()
	t.Logf("drained stop=%v cleanup=%v wait=%v root=%v output=%q", stopErr, cleanupErr, waitErr, cmd.ProcessState, output.stdout)
	output.mu.Unlock()
	if status != windows.WAIT_OBJECT_0 || observeErr != nil {
		t.Fatal("fixture root did not exit")
	}
}
