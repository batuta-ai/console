//go:build darwin || linux

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

type nativeStopUnixBirth struct {
	pid      int
	identity string
}

func nativeStopUnixObserve(t *testing.T, pid int) nativeStopUnixBirth {
	t.Helper()
	p, err := session.InspectProcess(pid)
	if err != nil || p.Missing || p.Identity == "" {
		t.Fatal("original process birth unavailable", pid, p, err)
	}
	return nativeStopUnixBirth{pid, p.Identity}
}

func (p nativeStopUnixBirth) alive(t *testing.T) bool {
	t.Helper()
	now, err := session.InspectProcess(p.pid)
	if err != nil {
		t.Fatal("process observation failed", p, err)
	}
	return !now.Missing && now.Identity == p.identity
}

func nativeStopUnixControl(a domain.PlanApproval) string {
	return filepath.Join(a.Identity.Binding.CanonicalRoot, ".git", "native-stop")
}

func nativeStopUnixGate(t *testing.T, a domain.PlanApproval) {
	t.Helper()
	dir := nativeStopUnixControl(a)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "release"), 0600); err != nil {
		t.Fatal(err)
	}
}

func nativeStopUnixProvider(t *testing.T) {
	t.Helper()
	cli, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cli)
	if err != nil {
		t.Fatal(err)
	}
	old := "printf 'approved output\\n' > out.txt"
	body := `control=$(git rev-parse --path-format=absolute --git-common-dir)/native-stop
/bin/sleep 300 &
child=$!
printf '%s %s\n' "$$" "$child" > "$control/birth.tmp"
mv "$control/birth.tmp" "$control/birth"
read release < "$control/release"
kill "$child"
wait "$child" 2>/dev/null
printf 'approved output\n' > out.txt`
	if strings.Count(string(data), old) != 1 {
		t.Fatal("qualified fixture completion changed")
	}
	if err := os.WriteFile(cli, []byte(strings.Replace(string(data), old, body, 1)), 0700); err != nil {
		t.Fatal(err)
	}
}

func nativeStopUnixTree(t *testing.T, ctx context.Context, a domain.PlanApproval) []nativeStopUnixBirth {
	t.Helper()
	var ids []string
	nativeStopAwait(t, ctx, "provider root and descendant readiness", func() bool {
		data, err := os.ReadFile(filepath.Join(nativeStopUnixControl(a), "birth"))
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		ids = strings.Fields(string(data))
		if len(ids) != 2 {
			t.Fatal("invalid birth marker", string(data))
		}
		return true
	})
	var tree []nativeStopUnixBirth
	for _, id := range ids {
		pid, err := strconv.Atoi(id)
		if err != nil {
			t.Fatal(err)
		}
		tree = append(tree, nativeStopUnixObserve(t, pid))
	}
	return tree
}

func nativeStopUnixRelease(t *testing.T, a domain.PlanApproval) {
	t.Helper()
	fd, err := syscall.Open(filepath.Join(nativeStopUnixControl(a), "release"), syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal("provider release has no waiting reader", err)
	}
	f := os.NewFile(uintptr(fd), "release")
	_, err = f.WriteString("continue\n")
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
}

func nativeStopUnixJournalLock(t *testing.T, a domain.PlanApproval, delivery string) func() {
	t.Helper()
	js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(js.Path(delivery), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if err := errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}

func TestNativeStopProcessesExactMissionResumeAndRestart(t *testing.T) {
	svc, a := nativeCompletedReviewFixture(t, true)
	account, err := svc.Store.GetAccount("acct_native")
	if err != nil {
		t.Fatal(err)
	}
	account.SessionCap = 2
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	b := nativeStopCloneMission(t, svc, a)
	nativeStopUnixGate(t, a)
	nativeStopUnixGate(t, b)
	nativeStopUnixProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	foreign := exec.Command("/bin/sleep", "300")
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
	unrelated := nativeStopUnixObserve(t, foreign.Process.Pid)
	first := nativeStopStart(t, ctx, svc, a)
	target := nativeStopUnixTree(t, ctx, a)
	second := nativeStopStart(t, ctx, svc, b)
	sibling := nativeStopUnixTree(t, ctx, b)
	if first.RunID == second.RunID {
		t.Fatal("two admissions share authority")
	}
	req := nativeStopReference(a, first)
	unlock := nativeStopUnixJournalLock(t, a, first.Delivery)
	caller, disconnect := context.WithCancel(ctx)
	pending, err := svc.StopExecution(caller, req)
	disconnect()
	if err != nil || pending.Effect != domain.ExecutionStopPending || pending.Finalized || pending.Durable {
		t.Fatal("accepted cancellation claimed completion", pending, err)
	}
	nativeStopAwait(t, ctx, "actual target group retirement", func() bool {
		for _, p := range target {
			if p.alive(t) {
				return false
			}
		}
		return true
	})
	nativeStopAwait(t, ctx, "actual Core journal unwinding blocked", func() bool {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\ngoroutine ") {
			if strings.Contains(stack, "journal.lockJournalContext") && strings.Contains(stack, "/core/loop.") {
				return true
			}
		}
		return false
	})
	held, err := svc.StopExecution(ctx, req)
	if err != nil || held.Effect != domain.ExecutionStopPending || held.Finalized || held.Durable {
		t.Fatal("held real unwinding not pending", held, err)
	}
	for _, p := range append(sibling, unrelated) {
		if !p.alive(t) {
			t.Fatal("exact Stop killed unrelated incarnation", p)
		}
	}
	unlock()
	final := nativeStopCanceledFinal(t, ctx, svc, req)
	if final.CoreState != loop.StateCanceled || !final.Durable {
		t.Fatal("actual canceled journal not recovered", final)
	}
	nativeStopUnixRelease(t, b)
	nativeStopReviewed(t, ctx, svc, b, second)
	resume := domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: first.Delivery, OpenedDigest: first.OpenedDigest}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			if err := svc.ShutdownNative(ctx); err != nil {
				t.Fatal(err)
			}
			if err := svc.Session.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if err := svc.Store.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(filepath.Join(svc.Home, "catalog.db"))
			if err != nil {
				t.Fatal(err)
			}
			old := svc
			svc = New(st, session.New(t.TempDir()), nil)
			svc.Home, svc.CoreExecutable, svc.NativeExecutorExecutable = old.Home, old.CoreExecutable, old.NativeExecutorExecutable
			// Requalify against a fresh synthetic TLS provider and the new manager.
			authenticateFixtureAccount(t, svc, account, "native-account")
			restarted := svc
			t.Cleanup(func() {
				drain, c := context.WithTimeout(context.Background(), 10*time.Second)
				defer c()
				if err := restarted.ShutdownNative(drain); err != nil {
					t.Error(err)
				}
				if err := restarted.Session.Shutdown(drain); err != nil {
					t.Error(err)
				}
				_ = st.Close()
			})
			recovered, err := svc.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
			if err != nil || recovered.Active || recovered.RunID != "" || recovered.OpenedDigest != first.OpenedDigest || recovered.Watch == nil || recovered.Watch.Header["state"] != loop.StateCanceled {
				t.Fatal("recreated catalog/journal recovery", recovered, err)
			}
		}
		if err := os.Remove(filepath.Join(nativeStopUnixControl(a), "birth")); err != nil {
			t.Fatal(err)
		}
		next, err := svc.ResumeExecution(ctx, resume)
		if err != nil || next.RunID == req.RunID || next.RunID == first.RunID || !domain.ValidExecutionRunID(next.RunID) {
			t.Fatal("resume did not get fresh authority", next, err)
		}
		tree := nativeStopUnixTree(t, ctx, a)
		stale, err := svc.StopExecution(ctx, req)
		if attempt == 0 {
			if err != nil || !stale.Finalized || stale.ExecutionStopRequest != req || (stale.State != nil && (stale.State.Active || stale.State.RunID != "")) {
				t.Fatal("historical Stop claimed replacement", stale, err)
			}
		} else if !errors.Is(err, domain.ErrInvalidReference) {
			t.Fatal("recreated daemon accepted old authority", stale, err)
		}
		for _, p := range tree {
			if !p.alive(t) {
				t.Fatal("stale Stop canceled replacement", p)
			}
		}
		state, err := svc.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
		if err != nil || !state.Active || state.RunID != next.RunID {
			t.Fatal("replacement authority lost", state, err)
		}
		req = nativeStopReference(a, next)
		nativeStopCanceledFinal(t, ctx, svc, req)
		for _, p := range tree {
			if p.alive(t) {
				t.Fatal("replacement original process survived Stop", p)
			}
		}
	}
	if !unrelated.alive(t) {
		t.Fatal("unrelated original process retired")
	}
	js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	records, err := js.Read(first.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	openings := 0
	for _, r := range records {
		if r.Kind == loop.KindOpened {
			openings++
		}
	}
	if openings != 1 {
		t.Fatal("resume/recreation duplicated approved opening", openings)
	}
	t.Log(fmt.Sprintf("real Core55: two missions; target+descendants stopped; sibling SHIP; three distinct admissions, one durable opening (%d records)", len(records)))
}

// Faults are introduced into this fixture's real resources after Stop has
// accepted its exact reference. No fabricated terminal or completion is used.
func TestNativeStopProcessesRetainResourceFailureCauses(t *testing.T) {
	for _, fault := range []string{"journal", "catalog", "ownership_cleanup"} {
		t.Run(fault, func(t *testing.T) {
			base, a := nativeCompletedReviewFixture(t, true)
			svc := New(base.Store, base.Session, nil)
			svc.Home, svc.CoreExecutable, svc.NativeExecutorExecutable = base.Home, base.CoreExecutable, base.NativeExecutorExecutable
			svc.Readiness = base.Readiness
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			t.Cleanup(func() {
				drain, c := context.WithTimeout(context.Background(), 10*time.Second)
				defer c()
				err := svc.ShutdownNative(drain)
				if fault == "catalog" {
					if err == nil || !strings.Contains(err.Error(), "database is closed") {
						t.Error("lost catalog cleanup cause at drain", err)
					}
				} else if err != nil {
					t.Error(err)
				}
			})
			nativeStopUnixGate(t, a)
			nativeStopUnixProvider(t)
			started := nativeStopStart(t, ctx, svc, a)
			svc.native.mu.Lock()
			owned := svc.native.jobs[started.Delivery]
			svc.native.mu.Unlock()
			if owned == nil || owned.runID != started.RunID {
				t.Fatal("actual admitted execution missing")
			}
			tree := nativeStopUnixTree(t, ctx, a)
			unlock := nativeStopUnixJournalLock(t, a, started.Delivery)
			req := nativeStopReference(a, started)
			pending, err := svc.StopExecution(ctx, req)
			if err != nil || pending.Effect != domain.ExecutionStopPending {
				t.Fatal(pending, err)
			}
			nativeStopAwait(t, ctx, "fault target retirement", func() bool {
				for _, p := range tree {
					if p.alive(t) {
						return false
					}
				}
				return true
			})
			js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
			if err != nil {
				t.Fatal(err)
			}
			cause := ""
			switch fault {
			case "journal":
				cause = batuta.ErrArtifactInvalid.Error()
				// Core has an opened descriptor blocked on our lock; corrupt that same
				// inode so its subsequent append/read must observe the real bad journal.
				f, err := os.OpenFile(js.Path(started.Delivery), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("not-a-journal-record\n")
				if err := errors.Join(err, f.Close()); err != nil {
					t.Fatal(err)
				}
			case "catalog":
				cause = "database is closed"
				if err := svc.Store.Close(); err != nil {
					t.Fatal(err)
				}
			case "ownership_cleanup":
				cause = "presence lock is not a regular file"
				lock := filepath.Join(a.Identity.Binding.CanonicalRoot, journal.Dir, started.Delivery+".lock")
				guard, err := os.OpenFile(lock+".guard", os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					guard.Close()
					t.Fatal(err)
				}
				if err := os.Remove(lock); err != nil {
					guard.Close()
					t.Fatal(err)
				}
				if err := os.Mkdir(lock, 0700); err != nil {
					guard.Close()
					t.Fatal(err)
				}
				if err := errors.Join(syscall.Flock(int(guard.Fd()), syscall.LOCK_UN), guard.Close()); err != nil {
					t.Fatal(err)
				}
			}
			unlock()
			waitNativeIdle(t, ctx, svc, started.Delivery)
			if fault == "catalog" {
				unavailable, err := svc.StopExecution(ctx, req)
				if err == nil || !strings.Contains(err.Error(), cause) || unavailable.Effect == domain.ExecutionStopCompleted {
					t.Fatal("fresh caller lost actual Store error", unavailable, err)
				}
				// All native work has joined before replacing the closed Store pointer.
				reopened, err := store.Open(filepath.Join(svc.Home, "catalog.db"))
				if err != nil {
					t.Fatal(err)
				}
				svc.Store = reopened
				t.Cleanup(func() { _ = reopened.Close() })
			}
			final := nativeStopFinal(t, ctx, svc, req)
			causes := strings.Join(final.Causes, "\n")
			if final.Effect != domain.ExecutionStopUncertain || final.Error == nil || !strings.Contains(causes, cause) {
				t.Fatalf("real %s failure was suppressed: %+v causes=%s", fault, final, causes)
			}
			if fault == "journal" && (final.Durable || final.CoreState != "" || !slices.Contains(final.Causes, cause) || !strings.Contains(causes, context.Canceled.Error())) {
				t.Fatal("invalid actual journal lost independent verification/cancellation evidence", final)
			}
			owned.outcomeMu.Lock()
			runErr, cleanupErr := owned.runErr, owned.cleanupErr
			owned.outcomeMu.Unlock()
			for _, actual := range []error{runErr, cleanupErr} {
				if actual != nil && !strings.Contains(causes, actual.Error()) {
					t.Fatal("public outcome lost actual per-job cause", actual, causes)
				}
			}
			if fault == "catalog" && (cleanupErr == nil || !strings.Contains(cleanupErr.Error(), cause)) {
				t.Fatal("catalog fault did not exercise actual finalization cleanup", cleanupErr)
			}
		})
	}
}

func TestNativeStopNilWrappedResourceFailureRemainsUncertain(t *testing.T) {
	svc, a, b := nativeStopTerminalFixture(t)
	js, err := journal.Open(b.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"canceled"}`), Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)}); err != nil {
		t.Fatal(err)
	}
	job, err := svc.native.admitExecution(domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, b.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: b.DeliveryID, RunID: job.runID}
	if result, err := svc.StopExecution(context.Background(), req); err != nil || result.Effect != domain.ExecutionStopPending {
		svc.native.finish(job)
		t.Fatal(result, err)
	}
	var absent error
	failure := fmt.Errorf("resource failed: %w", absent)
	job.recordRun(loop.StateCanceled, errors.Join(context.Canceled, failure))
	svc.finishNativeExecution(job, a)
	result, err := svc.StopExecution(context.Background(), req)
	if err != nil || result.Effect != domain.ExecutionStopUncertain || !result.Finalized || !result.Durable || result.CoreState != loop.StateCanceled || result.Error == nil || !strings.Contains(strings.Join(result.Causes, "\n"), failure.Error()) {
		t.Fatalf("independent resource error hidden by cancellation: %+v err=%v", result, err)
	}
}
