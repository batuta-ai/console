package session

import (
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func waitForUnreapedExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		// Read kernel state independently of InspectProcess. Darwin sys/proc.h
		// defines SZOMB (5) as awaiting collection by the parent.
		record, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil {
			t.Fatal(err)
		}
		if int(record.Proc.P_pid) != pid || int(record.Eproc.Ppid) != os.Getpid() {
			t.Fatalf("child identity/parent changed: %+v", record)
		}
		if record.Proc.P_stat == 5 {
			t.Logf("native kern.proc.pid: PID %d parent %d state SZOMB before Wait", pid, record.Eproc.Ppid)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child did not reach SZOMB after EOF: state %d", record.Proc.P_stat)
		}
		runtime.Gosched()
	}
}
