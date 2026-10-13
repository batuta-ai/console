package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsProcessBirthObservation(t *testing.T) {
	argv := task43ProbeArgv(t, "hold")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "USERPROFILE=" + os.Getenv("USERPROFILE")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	var created, exited, kernel, user windows.Filetime
	var observeErr error
	var observed windows.Handle
	if err := cmd.Process.WithHandle(func(handle uintptr) {
		observeErr = windows.GetProcessTimes(windows.Handle(handle), &created, &exited, &kernel, &user)
		if observeErr == nil {
			observeErr = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(handle), windows.CurrentProcess(), &observed, windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0)
		}
	}); err != nil || observeErr != nil {
		t.Fatal(err, observeErr)
	}
	defer windows.CloseHandle(observed)
	want := fmt.Sprintf("windows:%d:%d", cmd.Process.Pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	a, err := InspectProcess(cmd.Process.Pid)
	if err != nil || a.Missing || a.Identity != want {
		t.Fatal("native birth observation disagrees with original creation handle", a, want, err)
	}
	b, err := InspectProcess(cmd.Process.Pid)
	if err != nil || b != a {
		t.Fatal("native birth identity changed", a, b, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if status, err := windows.WaitForSingleObject(observed, 2000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("owned fixture did not terminate", status, err)
	}
	dead, err := InspectProcess(cmd.Process.Pid)
	if err != nil || !dead.Missing || dead.Identity != "" {
		t.Fatal("unreaped terminated native process guessed live", dead, err)
	}
	for _, pid := range []int{0, -1, 1 << 32} {
		if observation, err := InspectProcess(pid); err == nil || observation != (ProcessObservation{}) {
			t.Fatal("invalid native PID guessed absent or live", pid, observation, err)
		}
	}
}

func TestTask43WindowsPendingBirthObservation(t *testing.T) {
	home := task43SessionHome(t)
	m := New(t.TempDir())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	p, err := m.StartPending(domain.Session{ID: "native-observed", Argv: task43ConsoleArgv(t), ProcessIdentity: "untrusted", RecoveryReason: "obsolete"}, nil, home, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := p.Snapshot()
	actual, err := InspectProcess(before.PID)
	if err != nil || before.ProcessIdentity == "" || before.ProcessIdentity != actual.Identity || before.RecoveryReason != "" {
		t.Fatal("native launch did not replace supplied identity with a kernel observation", before, actual, err)
	}
	if _, err := m.Stop("native-observed"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Persist(func(after domain.Session) error {
		if after.Process != "exited" || after.EndedAt == nil || after.ProcessIdentity != before.ProcessIdentity {
			t.Error("native exit lost its observed birth identity", before, after)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
