//go:build darwin || linux

package session

import (
	"os/exec"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeProcessIncarnation(t *testing.T) {
	cmd := exec.Command("/bin/cat")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if cmd.ProcessState == nil {
			cmd.Wait()
		}
	}()
	a, err := InspectProcess(cmd.Process.Pid)
	if err != nil || a.Identity == "" || a.Missing {
		t.Fatalf("live native identity: %+v %v", a, err)
	}
	b, err := InspectProcess(cmd.Process.Pid)
	if err != nil || a != b {
		t.Fatalf("unstable identity: %+v %+v %v", a, b, err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	waitForUnreapedExit(t, cmd.Process.Pid)
	absent, err := InspectProcess(cmd.Process.Pid)
	if err != nil || !absent.Missing || absent.Identity != "" {
		t.Fatalf("unreaped terminated process: %+v %v", absent, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	t.Logf("native live identity %s; unreaped child observed missing, then reaped successfully", a.Identity)
	if _, err := InspectProcess(-1); err == nil {
		t.Fatal("invalid PID guessed absent")
	}
}

func TestPendingCapturesObservedIdentity(t *testing.T) {
	m := New(t.TempDir())
	p, err := m.StartPending(domain.Session{ID: "identity", Argv: []string{"/bin/cat"}, ProcessIdentity: "untrusted", RecoveryReason: "obsolete"}, nil, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop("identity")
	before := p.Snapshot()
	observed, err := InspectProcess(before.PID)
	if err != nil || before.ProcessIdentity == "" || before.ProcessIdentity != observed.Identity || before.RecoveryReason != "" {
		t.Fatalf("launch not observed: %+v %+v %v", before, observed, err)
	}
	if _, err := m.Stop("identity"); err != nil {
		t.Fatal(err)
	}
	_, err = p.Persist(func(terminal domain.Session) error {
		if terminal.Process != "exited" || terminal.EndedAt == nil || terminal.ProcessIdentity != before.ProcessIdentity {
			t.Errorf("exit lost identity: %+v", terminal)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
