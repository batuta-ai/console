//go:build darwin || linux

package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"golang.org/x/sys/unix"
)

// The child ignores inherited terminal HUP and holds an independent FIFO open.
// EOF proves that child released its descriptors; PTY EOF alone cannot prove it.
// A control FIFO lets even the red implementation's orphan exit without a PID kill.
func groupFixture(t *testing.T, m *Manager, id string) (*Pending, <-chan error) {
	t.Helper()
	dir := t.TempDir()
	ready, control := filepath.Join(dir, "ready"), filepath.Join(dir, "control")
	for _, path := range []string{ready, control} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctl, err := os.OpenFile(control, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan struct{})
	t.Cleanup(func() {
		_, _ = ctl.WriteString("quit\n")
		_ = ctl.Close()
		select {
		case <-childDone:
		case <-time.After(5 * time.Second):
			t.Error("fixture child cleanup watchdog")
		}
	})
	pidCh := make(chan int, 1)
	eof := make(chan error, 1)
	go func() {
		defer close(childDone)
		f, err := os.Open(ready)
		if err != nil {
			eof <- err
			return
		}
		defer f.Close()
		r := bufio.NewReader(f)
		line, err := r.ReadString('\n')
		if err != nil {
			eof <- err
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			eof <- err
			return
		}
		pidCh <- pid
		_, err = r.ReadByte()
		eof <- err
	}()
	script := `trap '' HUP; /bin/sh -c 'exec 3>"$1"; printf "%s\n" "$$" >&3; read answer <"$2"' child "$1" "$2" & read answer`
	p, err := m.StartPending(domain.Session{ID: id, AccountID: id, Argv: []string{"/bin/sh", "-c", script, "leader", ready, control}}, nil, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Stop(id) })
	select {
	case child := <-pidCh:
		pgid, err := unix.Getpgid(child)
		if err != nil || pgid != p.Snapshot().PID {
			t.Fatalf("child %d group %d leader %d: %v", child, pgid, p.Snapshot().PID, err)
		}
		t.Logf("owned leader=%d child=%d original pgid=%d; child inherits ignored HUP", p.Snapshot().PID, child, pgid)
	case <-time.After(5 * time.Second):
		t.Fatal("child readiness watchdog")
	}
	return p, eof
}

func requireChildEOF(t *testing.T, eof <-chan error) {
	t.Helper()
	select {
	case err := <-eof:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("child descriptor closure: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HUP-ignoring original-group child survived leader termination")
	}
}

func TestOriginalGroupStopAndNaturalExit(t *testing.T) {
	for _, natural := range []bool{false, true} {
		t.Run(fmt.Sprint(natural), func(t *testing.T) {
			m := New(t.TempDir())
			callbacks := make(chan domain.Session, 4)
			m.OnExit = func(s domain.Session) { callbacks <- s }
			sibling, err := m.Start(domain.Session{ID: "sibling", Argv: []string{"/bin/cat"}}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			defer m.Stop(sibling.ID)
			foreign := exec.Command("/bin/cat")
			in, err := foreign.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := foreign.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { in.Close(); _ = foreign.Wait() }()
			p, eof := groupFixture(t, m, "tree")
			if natural {
				if _, err := m.Take("tree", "tui", "owner"); err != nil {
					t.Fatal(err)
				}
				if err := m.Input("tree", "owner", []byte("exit\n")); err != nil {
					t.Fatal(err)
				}
			} else if _, err := m.Stop("tree"); err != nil {
				t.Fatal(err)
			}
			select {
			case s := <-callbacks:
				if s.ID != "tree" || s.Process != "exited" || s.EndedAt == nil {
					t.Fatalf("terminal callback: %+v", s)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("leader wait watchdog")
			}
			requireChildEOF(t, eof)
			for _, pid := range []int{sibling.PID, foreign.Process.Pid} {
				obs, err := InspectProcess(pid)
				if err != nil || obs.Missing {
					t.Fatalf("unrelated process %d affected: %+v %v", pid, obs, err)
				}
			}
			s := p.Snapshot()
			if s.Process != "exited" || s.EndedAt == nil {
				t.Fatalf("snapshot %+v", s)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.Shutdown(ctx); err != nil {
				t.Fatalf("group lifecycle cleanup: %v", err)
			}
		})
	}
}
