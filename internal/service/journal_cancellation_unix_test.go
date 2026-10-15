//go:build darwin || linux

package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestServiceJournalProcessCancellationDrain(t *testing.T) {
	svc, a, b := cancellationJournalFixture(t, false)
	svc.Session = session.New(t.TempDir())
	svc.Readiness = nil
	svc.Home = testfixture.PrivateHome(t)
	account := domain.Account{ID: "acct_journal", Adapter: "codex", Connected: true, SessionCap: 1}
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	ad, err := adapters.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.IsolateEnv(svc.Home, account); err != nil {
		t.Fatal(err)
	}
	mission, err := svc.Store.GetMission("m")
	if err != nil {
		t.Fatal(err)
	}
	mission.SquadSnapshot = domain.Squad{ID: "q", Members: []domain.Member{{ID: "member", CLI: "codex", Model: "qa-native", AccountID: account.ID, Maestro: true}}}
	if err := svc.Store.PutMission(mission); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := svc.ShutdownNative(ctx); err != nil {
			t.Error(err)
		}
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	release := lockObservationJournal(t, b)
	defer release()
	fifo := filepath.Join(t.TempDir(), "readiness-ready")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	holdFIFO := filepath.Join(t.TempDir(), "readiness-hold")
	if err := syscall.Mkfifo(holdFIFO, 0600); err != nil {
		t.Fatal(err)
	}
	holdPipe, err := os.OpenFile(holdFIFO, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holdPipe.Close()
	core := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(core, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" > '%s'\nexec /bin/cat '%s'\n", fifo, holdFIFO)), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(core)+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := make(chan nativeStartOutcome, 1)
	go func() {
		result, err := svc.StartExecution(context.Background(), domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
		started <- nativeStartOutcome{result, err}
	}()
	// The FIFO handshake is emitted by the owned readiness process itself.
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(pipe).ReadString('\n'); ready <- line }()
	var pid int
	select {
	case line := <-ready:
		pid, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			t.Fatal("invalid process handshake", line, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("owned readiness process did not signal readiness")
	}
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := svc.ShutdownNative(ctx); err != nil {
			t.Error("original job cleanup:", err)
		}
		svc.native.mu.Lock()
		remaining := len(svc.native.jobs)
		svc.native.mu.Unlock()
		if remaining != 0 {
			t.Error("original admission survived cleanup", remaining)
		}
		reserve, err := svc.Session.ReserveNative(map[string]int{"acct_journal": 1})
		if err != nil {
			t.Error("account reservation survived cleanup", err)
		} else {
			reserve()
		}
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error("owned session cleanup:", err)
		}
		if observation, err := session.InspectProcess(pid); err != nil || !observation.Missing {
			t.Error("owned readiness process survived cleanup", observation, err)
		}
		lockObservationJournal(t, b)()
	})
	svc.native.mu.Lock()
	job := svc.native.jobs[b.DeliveryID]
	svc.native.mu.Unlock()
	if job == nil || job.ctx.Err() != nil {
		t.Fatal("original readiness job was not live and admitted")
	}
	if deadline, ok := job.ctx.Deadline(); ok {
		t.Fatal("original readiness job unexpectedly has deadline", deadline)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- svc.ShutdownNative(drainCtx) }()
	// The writer stays locked from before readiness through the joined result.
	// Only detached finalization can contribute DeadlineExceeded: the original
	// job has no deadline and shutdown cancels it with context.Canceled.
	if err := <-drained; err != nil {
		t.Error("cancelled process job did not drain with lock held:", err)
		release()
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := svc.ShutdownNative(cleanup); err != nil {
			t.Fatal("original job cleanup:", err)
		}
	}
	outcome := <-started
	if context.Cause(job.ctx) != context.Canceled {
		t.Error("original job did not receive shutdown cancellation", context.Cause(job.ctx))
	}
	if !errors.Is(outcome.err, context.DeadlineExceeded) || !strings.Contains(outcome.err.Error(), "journal: lock: context deadline exceeded") {
		t.Error("held-lock finalization did not return its exact local timeout", outcome.err)
	}
	if !errors.Is(outcome.err, domain.ErrExecutionUncertain) || outcome.result.Effect != domain.ExecutionUncertain || outcome.result.Delivery != b.DeliveryID || outcome.result.OpenedDigest != "" {
		t.Error("bounded failure lost uncertainty/original cause", outcome)
	}
	if code, ok := ReadinessFailure(outcome.err); !ok || code != "check_cancelled" {
		t.Error("lost original readiness cancellation", outcome.err)
	}
	svc.native.mu.Lock()
	remaining := len(svc.native.jobs)
	svc.native.mu.Unlock()
	if remaining != 0 {
		t.Error("original admission retained", remaining)
	}
	reserve, err := svc.Session.ReserveNative(map[string]int{"acct_journal": 1})
	if err != nil {
		t.Fatal("native account reservation retained", err)
	}
	reserve()
	if observation, err := session.InspectProcess(pid); err != nil || !observation.Missing {
		t.Fatal("owned readiness process survived drain", observation, err)
	}
	release()
	lockObservationJournal(t, b)()
}
