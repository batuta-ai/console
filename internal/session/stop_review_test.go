package session

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/domain"
)

// The barrier delays the return from a real PTY Close, without changing its
// result. This exercises the native-resource waiter, not a simulated Job failure.
type stopCloseBarrier struct {
	terminalPTY
	entered chan struct{}
	release <-chan struct{}
}

func (p *stopCloseBarrier) Close() error {
	err := p.terminalPTY.Close()
	close(p.entered)
	<-p.release
	return err
}

func TestStopFinalizationNativeUncertaintyCanResolve(t *testing.T) {
	env, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Errorf("private home cleanup: %v", err)
		}
	})
	m := New(t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	argv := sessionTestArgv(t, []string{"/bin/cat"})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = accounts.RuntimeEnvironment(os.Environ(), env)
	pty, owned, err := startManagedPTY(cmd, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	pty, inputErr := prepareTerminalPTY(pty)
	log, err := os.CreateTemp(t.TempDir(), "transcript-")
	if err != nil {
		_ = owned.stop()
		_ = waitOwned(cmd, owned)
		_ = pty.Close()
		t.Fatal(err)
	}
	live := &live{sess: domain.Session{ID: "uncertain", PID: ownedProcessID(cmd, owned), Process: "running", Owner: "none"}, cmd: cmd, pty: terminalFile(pty), terminal: &stopCloseBarrier{pty, entered, release}, inputErr: inputErr, log: log, done: make(chan struct{}), nativeDone: make(chan struct{}), readDone: make(chan struct{}), owned: owned, privateHome: home}
	m.mu.Lock()
	m.live[live.sess.ID] = live
	m.active.Add(1)
	m.mu.Unlock()
	go live.readLoop(m)
	go live.wait(m)
	finalize := func(id string, row domain.Session, err error) (domain.Session, error) {
		if err == os.ErrNotExist {
			return domain.Session{ID: id, Process: "exited"}, nil
		}
		return row, err
	}
	done := make(chan error, 1)
	go func() { _, err := m.StopAndFinalize(context.Background(), "uncertain", finalize); done <- err }()
	awaitStopPhase(t, entered)
	err = awaitStopPhase(t, done)
	if err == nil || !strings.Contains(err.Error(), "termination did not complete") {
		t.Fatalf("held real PTY close did not reach native uncertainty: %v", err)
	}
	select {
	case <-live.nativeDone:
		t.Fatal("native phase completed while Close return held")
	default:
	}
	unblock()
	awaitStopPhase(t, live.done)
	row, err := m.StopAndFinalize(context.Background(), "uncertain", finalize)
	if err != nil || row.Process != "exited" {
		t.Fatalf("resolved native uncertainty stayed definitive: %+v %v", row, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stopFailures) != 0 || len(m.stops) != 0 {
		t.Fatal("resolved uncertainty retained failure or completed job")
	}
}
