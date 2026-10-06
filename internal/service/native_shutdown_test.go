//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
)

func TestNativeDaemonLifetimeDrainsActualCoreModelGroup(t *testing.T) {
	svc, approval, _ := nativeRunFixture(t)
	cli, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(bytes), "printf 'BATUTA-QUESTION: which approved output?\\n'", `/bin/sleep 30 &
printf '%s\n' "$!" > "$HOME/native-child"
wait`, 1)
	if err := os.WriteFile(cli, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started, err := svc.StartExecution(ctx, domain.ExecutionRequest{Target: approval.Identity, ApprovalID: approval.ID})
	if err != nil {
		t.Fatal(started, err)
	}
	adapter, _ := adapters.Get("codex")
	marker := filepath.Join(adapter.IsolateDir(svc.Home, "acct_native"), "native-child")
	pid := 0
	for pid == 0 {
		bytes, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(bytes)))
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual Core model descendant did not start")
		case <-time.After(time.Millisecond):
		}
	}
	svc.Session.CloseAdmission()
	if err := svc.ShutdownNative(ctx); err != nil {
		t.Fatal("native job did not drain", err)
	}
	if err := svc.Session.Shutdown(ctx); err != nil {
		t.Fatal("native process manager did not drain", err)
	}
	if observation, err := session.InspectProcess(pid); err != nil || !observation.Missing {
		t.Fatal("native model descendant survived completed drain", observation, err)
	}
	mission, err := svc.Store.GetMission("m")
	if err != nil || mission.Binding == nil || mission.Binding.DeliveryID != started.Delivery || mission.Binding.OpenedDigest != started.OpenedDigest {
		t.Fatal("drain lost durable native identity", mission, err)
	}
	if _, err := svc.ResumeExecution(ctx, domain.ResumeRequest{Target: approval.Identity, ApprovalID: approval.ID, Delivery: started.Delivery, OpenedDigest: started.OpenedDigest}); !errors.Is(err, domain.ErrExecutionClosing) {
		t.Fatal("closed native admission accepted resume", err)
	}
}
