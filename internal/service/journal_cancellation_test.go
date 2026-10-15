package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func cancellationJournalFixture(t *testing.T, bound bool) (*Service, domain.PlanApproval, domain.MissionBinding) {
	t.Helper()
	svc, _ := approvalFixture(t)
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	b := appendCancellationJournal(t, a)
	if bound {
		if err := svc.Store.BindMission("m", b); err != nil {
			t.Fatal(err)
		}
	}
	if digest, err := batuta.ValidateApprovedDelivery(b.CanonicalRoot, b, a); err != nil || digest != b.OpenedDigest {
		t.Fatalf("invalid fixture opening: %q %v", digest, err)
	}
	return svc, a, b
}

func appendCancellationJournal(t *testing.T, a domain.PlanApproval) domain.MissionBinding {
	t.Helper()
	b := a.Identity.Binding
	b.DeliveryID = batuta.ExecutionDelivery(a)
	js, err := journal.Open(b.CanonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"slug": b.PlanSlug, "plan_path": b.PlanPath, "plan_digest": b.PlanDigest, "workspace": b.CanonicalRoot, "approval_receipt_id": a.ID, "plan_content_digest": a.Identity.Version})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: data, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	b.OpenedDigest = opened.Digest

	return b
}

func lockObservationJournal(t *testing.T, b domain.MissionBinding) func() {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(b.CanonicalRoot, journal.Dir, b.DeliveryID+".jsonl"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := holdObservationJournal(file)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if err := unlock(); err != nil {
			t.Error("unlock owned journal:", err)
		}
		if err := file.Close(); err != nil {
			t.Error("close owned journal:", err)
		}
	}
	t.Cleanup(release)
	return release
}

// Wait only returns when the operation is durably blocked. The Core frame
// distinguishes a real busy-lock retry from a fixture/database precondition.
func requireJournalWait(t *testing.T, caller string) {
	t.Helper()
	synctest.Wait()
	stack := make([]byte, 1<<20)
	stack = stack[:runtime.Stack(stack, true)]
	for _, goroutine := range strings.Split(string(stack), "\n\n") {
		if strings.Contains(goroutine, "core/journal.lockJournalContext") && strings.Contains(goroutine, caller) {
			return
		}
	}
	t.Fatalf("operation never waited on the held journal lock (%s):\n%s", caller, stack)
}

func TestServiceJournalObservationCancellation(t *testing.T) {
	for _, cancellation := range []struct {
		name  string
		cause error
	}{{"cancel", context.Canceled}, {"cause", errors.New("observation cancelled after lock wait")}} {
		t.Run(cancellation.name, func(t *testing.T) {
			for _, operation := range []string{"state", "plan", "answer", "projection", "link", "approved projection", "legacy projection", "reference", "validation"} {
				t.Run(operation, func(t *testing.T) {
					svc, a, b := cancellationJournalFixture(t, true)
					release := lockObservationJournal(t, b)
					synctest.Test(t, func(t *testing.T) {
						ctx, cancel := context.WithCancelCause(context.Background())
						defer cancel(nil)
						cause := cancellation.cause
						done := make(chan error, 1)
						go func() {
							var err error
							switch operation {
							case "state":
								var state domain.ExecutionState
								state, err = svc.ExecutionState(ctx, "m", "w", a.ID)
								if state.Watch != nil || state.Gate != nil || state.OpenedDigest != "" {
									t.Error("cancelled state fabricated snapshot", state)
								}
							case "plan":
								var detail domain.PlanDetail
								detail, err = svc.PlanDetail(ctx, "m", "w")
								if detail.Approval != nil || len(detail.Content) != 0 {
									t.Error("cancelled detail exposed approval", detail)
								}
							case "answer":
								var result domain.AnswerResult
								result, err = svc.AnswerQuestion(ctx, domain.AnswerRequest{MissionID: "m", WorkspaceID: "w", Delivery: b.DeliveryID, OpenedDigest: b.OpenedDigest, Task: "1", Execution: 1, QuestionID: b.OpenedDigest, Text: "answer"})
								if result.Effect != domain.AnswerRejected || result.Delivery != "" {
									t.Error("cancelled preflight accepted answer", result)
								}
							case "approved projection", "legacy projection":
								var view domain.WatchView
								if operation == "approved projection" {
									view, err = batuta.ProjectApprovedMissionContext(ctx, b.CanonicalRoot, b, a)
								} else {
									view, err = batuta.ProjectMissionContext(ctx, b.CanonicalRoot, b)
								}
								if view.Available || view.Source != domain.SourceUnavailable || view.Progression != nil {
									t.Error("cancelled projection exposed snapshot", view)
								}
							case "reference":
								var reference batuta.DeliveryApproval
								reference, err = batuta.DeliveryApprovalReferenceContext(ctx, b.CanonicalRoot, b)
								if reference != (batuta.DeliveryApproval{}) {
									t.Error("cancelled reference exposed receipt", reference)
								}
							case "validation":
								var digest string
								digest, err = batuta.ValidateApprovedDeliveryContext(ctx, b.CanonicalRoot, b, a)
								if digest != "" {
									t.Error("cancelled validation exposed opening", digest)
								}
							case "projection":
								var view domain.WatchView
								view, err = svc.projectMission(ctx, "m", "w", b)
								if view.Available || view.Source != domain.SourceUnavailable {
									t.Error("cancelled projection available", view)
								}
							case "link":
								result := executionFailure(domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, nil)
								job := &nativeExecution{mission: "m", delivery: b.DeliveryID, ready: make(chan nativeStartOutcome, 1)}
								err = svc.linkNativeExecution(ctx, job, a, &result)
								if result.Effect != domain.ExecutionRejected || result.OpenedDigest != "" {
									t.Error("cancelled link accepted opening", result)
								}
								select {
								case <-job.ready:
									t.Error("cancelled link signalled success")
								default:
								}
							}
							done <- err
						}()
						defer release()
						requireJournalWait(t, "console/internal/service.")
						cancel(cause)
						synctest.Wait()
						var got error
						select {
						case got = <-done:
						default:
							t.Error("operation remained blocked after cancellation while writer held lock")
							release()
							got = <-done
						}
						if !errors.Is(got, cause) {
							t.Errorf("cancellation identity lost: got %v, want %v", got, cause)
						}
					})
					// Reacquiring with a fresh descriptor proves neither observer nor owner leaked.
					lockObservationJournal(t, b)()
				})
			}
		})
	}
}

func TestServiceJournalFinalizationDrain(t *testing.T) {
	svc, a, b := cancellationJournalFixture(t, false)
	svc.Session = session.New(t.TempDir())
	release := lockObservationJournal(t, b)
	synctest.Test(t, func(t *testing.T) {
		svc.native = newNativeExecutions()
		defer release()
		started := make(chan nativeStartOutcome, 1)
		go func() {
			result, err := svc.StartExecution(context.Background(), domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
			started <- nativeStartOutcome{result, err}
		}()
		requireJournalWait(t, "signalNativeExecutionFailure")
		svc.native.mu.Lock()
		job := svc.native.jobs[b.DeliveryID]
		svc.native.mu.Unlock()
		if job == nil {
			t.Fatal("original native job was not admitted")
		}
		drained := make(chan error, 1)
		go func() { drained <- svc.ShutdownNative(context.Background()) }()
		synctest.Wait()
		if !errors.Is(job.ctx.Err(), context.Canceled) {
			t.Error("drain did not cancel original job")
		}
		select {
		case err := <-drained:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("native finalization did not drain while writer retained journal lock")
			release()
			if err := <-drained; err != nil {
				t.Error(err)
			}
		}
		outcome := <-started
		if !errors.Is(outcome.err, domain.ErrExecutionUncertain) || !errors.Is(outcome.err, domain.ErrExecutionUnavailable) || outcome.result.Effect != domain.ExecutionUncertain || outcome.result.Delivery != b.DeliveryID {
			t.Error("lost original failure/uncertainty", outcome)
		}
		if !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Error("lost finalization deadline", outcome.err)
		}
		if outcome.result.OpenedDigest != "" {
			t.Error("blocked finalization invented validated opening", outcome.result)
		}
		svc.native.mu.Lock()
		remaining := len(svc.native.jobs)
		svc.native.mu.Unlock()
		if remaining != 0 {
			t.Error("original job admission was not released", remaining)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error("owned session cleanup:", err)
		}
	})
	lockObservationJournal(t, b)()
}

func TestServiceJournalCompatibilityBound(t *testing.T) {
	for _, operation := range []string{"WatchMission", "Watch", "BindMission"} {
		t.Run(operation, func(t *testing.T) {
			svc, _, b := cancellationJournalFixture(t, operation != "BindMission")
			release := lockObservationJournal(t, b)
			synctest.Test(t, func(t *testing.T) {
				defer release()
				done := make(chan error, 1)
				start := time.Now()
				go func() {
					var err error
					if operation == "BindMission" {
						_, err = svc.BindMission("m", MissionBindingRequest{WorkspaceID: "w", PlanPath: b.PlanPath, DeliveryID: b.DeliveryID})
					} else {
						var view domain.WatchView
						if operation == "Watch" {
							view, err = svc.Watch("w", b.DeliveryID)
						} else {
							view, err = svc.WatchMission("m", "w", b.DeliveryID)
						}
						if view.Available || (operation == "WatchMission" && view.Source != domain.SourceUnavailable) {
							t.Error("timed out watch exposed snapshot", view)
						}
					}
					done <- err
				}()
				requireJournalWait(t, operation)
				var err error
				select {
				case err = <-done:
				case <-time.After(6 * time.Second):
					t.Error("compatibility observer has unbounded journal observation")
					release()
					err = <-done
				}
				if !errors.Is(err, batuta.ErrArtifactIO) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					t.Error("local observation budget must be artifact I/O, not client cancellation", err)
				}
				if time.Since(start) != 5*time.Second {
					t.Errorf("budget = %v, want 5s", time.Since(start))
				}
			})
			lockObservationJournal(t, b)()
			if operation == "BindMission" {
				m, err := svc.Store.GetMission("m")
				if err != nil || m.Binding == nil || m.Binding.DeliveryID != "" {
					t.Error("timed out bind changed mission", m, err)
				}
			}
		})
	}
}

func TestServiceJournalFinalizationClassification(t *testing.T) {
	for _, mode := range []string{"validated", "missing", "active missing", "truncated", "existence error"} {
		t.Run(mode, func(t *testing.T) {
			svc, a, b := cancellationJournalFixture(t, false)
			path := filepath.Join(b.CanonicalRoot, journal.Dir, b.DeliveryID+".jsonl")
			switch mode {
			case "missing", "active missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.WriteFile(path, []byte(`{"partial":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "existence error":
				if err := os.RemoveAll(b.CanonicalRoot); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			job := &nativeExecution{ctx: ctx, delivery: b.DeliveryID, ready: make(chan nativeStartOutcome, 1)}
			original := errors.New("original execution failure")
			result := executionFailure(domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, original)
			svc.signalNativeExecutionFailure(job, a, result, mode == "active missing", original)
			outcome := <-job.ready
			if !errors.Is(outcome.err, original) {
				t.Fatal("original failure lost", outcome)
			}
			uncertain := mode != "missing"
			if errors.Is(outcome.err, domain.ErrExecutionUncertain) != uncertain {
				t.Error("uncertainty classification changed", outcome)
			}
			if uncertain {
				if outcome.result.Effect != domain.ExecutionUncertain || outcome.result.Delivery != b.DeliveryID {
					t.Error("uncertain evidence lost", outcome)
				}
			} else if outcome.result.Effect != domain.ExecutionRejected || outcome.result.Delivery != "" {
				t.Error("missing inactive evidence invented execution", outcome)
			}
			if mode == "validated" {
				if outcome.result.OpenedDigest != b.OpenedDigest {
					t.Error("cancelled job lost independently validated opening", outcome)
				}
			} else if outcome.result.OpenedDigest != "" {
				t.Error("invalid evidence invented opening", outcome)
			}
		})
	}
}
