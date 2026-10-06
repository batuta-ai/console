//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeRunOutlivesCanceledCallerAfterAdmission(t *testing.T) {
	svc, a, _ := nativeRunFixture(t)
	ad, err := adapters.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	home := ad.IsolateDir(svc.Home, "acct_native")
	pause := filepath.Join(home, "pause-version")
	if err := os.WriteFile(pause, []byte("wait"), 0600); err != nil {
		t.Fatal(err)
	}
	cli, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	gate := `if [ "$1" = --version ]; then
while [ -f "$HOME/pause-version" ]; do
printf 'waiting\n' > "$HOME/version-waiting"
/bin/sleep 0.01
done
fi
`
	updated := strings.Replace(string(bytes), "#!/bin/sh\n", "#!/bin/sh\n"+gate, 1)
	observedHome := filepath.Join(svc.Home, "observed-home")
	updated = strings.Replace(updated, "printf 'submission\\n'", "printf '%s\\n' \"$HOME\" > '"+strings.ReplaceAll(observedHome, "'", "'\\''")+"'\nprintf 'submission\\n'", 1)
	if err := os.WriteFile(cli, []byte(updated), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	done := make(chan nativeStartOutcome, 1)
	go func() {
		result, err := svc.StartExecution(requestCtx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
		done <- nativeStartOutcome{result, err}
	}()
	for {
		if _, err := os.Stat(filepath.Join(home, "version-waiting")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("admitted native preparation never started")
		case <-time.After(time.Millisecond):
		}
	}
	cancelRequest()
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, domain.ErrExecutionUncertain) || outcome.result.Effect != domain.ExecutionUncertain || outcome.result.Delivery != domain.ExecutionDelivery(a.ID, a.Identity) {
			t.Fatal("caller cancellation hid admitted native operation", outcome)
		}
	case <-ctx.Done():
		t.Fatal("canceled caller did not return")
	}
	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	question := waitNativeQuestionAndIdle(t, ctx, svc, a, domain.ExecutionDelivery(a.ID, a.Identity), 0)
	if question.Prompt != "which approved output?" {
		t.Fatal("native executor question missing", question)
	}
	launches, err := os.ReadFile(filepath.Join(home, "native-launches"))
	if err != nil || string(launches) != "submission\n" {
		actualHome, _ := os.ReadFile(observedHome)
		t.Fatal("caller cancellation lost or duplicated native submission", string(launches), err, question, string(actualHome))
	}
}
