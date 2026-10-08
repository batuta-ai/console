package session

import (
	"context"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestTask43WindowsConPTYOriginalExitDiagnosis(t *testing.T) {
	home := task43SessionHome(t)
	m := New(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	p, err := m.StartPending(domain.Session{ID: "native-exit-observer", Argv: task43ConsoleArgv(t), Cols: 80, Rows: 24}, nil, home, 1)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.live.done:
		t.Logf("native original root pid=%d exited=%v code=%x cleanup=%v consoleError=%v replay=%q", p.live.owned.pid, p.live.owned.exited, p.live.owned.exitCode, p.live.cleanupErr, p.live.terminal.(*windowsTerminal).pumpErr, p.live.replay)
		t.Fatal("native console root exited before interactive input")
	case <-ctx.Done():
		if _, err := m.Stop("native-exit-observer"); err != nil {
			t.Fatal(err)
		}
	}
}
