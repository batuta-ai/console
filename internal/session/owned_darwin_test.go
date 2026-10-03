package session

import (
	"errors"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinZombieOnlyOwnedGroup(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	terminal, err := startPTY(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	}()
	owned := newOwnedProcess(cmd)
	waitForUnreapedExit(t, cmd.Process.Pid)
	records, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", cmd.Process.Pid)
	if err != nil || len(records) != 1 {
		t.Fatalf("independent group observation: %v %v", records, err)
	}
	p := records[0]
	if int(p.Proc.P_pid) != cmd.Process.Pid || int(p.Eproc.Pgid) != cmd.Process.Pid || p.Proc.P_stat != 5 {
		t.Fatalf("not the unreaped zombie-only original group: %+v", p)
	}
	if err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL); !errors.Is(err, unix.EPERM) {
		t.Fatalf("expected native Darwin zombie-only EPERM, got %v", err)
	}
	t.Logf("independent native group: PID=PGID=%d SZOMB; kill returned EPERM before sole Wait", cmd.Process.Pid)
	if err := owned.stop(); err != nil {
		t.Errorf("already-terminal owned group: %v", err)
	}
	if err := waitOwned(cmd, owned); err != nil {
		t.Fatalf("already-terminal lifecycle cleanup: %v", err)
	}
	if !cmd.ProcessState.Success() || !owned.retired {
		t.Fatal("sole Wait did not complete after retirement")
	}
	if err := owned.stop(); err != nil {
		t.Fatalf("retired stop: %v", err)
	}
}
