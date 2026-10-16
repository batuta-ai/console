package service

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
)

func TestServiceJournalContextObservers(t *testing.T) {
	for _, operation := range []string{"Watch", "WatchMission", "BindMission"} {
		for _, mode := range []string{"cancel", "cause", "artifact cause", "deadline"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				svc, _, b := cancellationJournalFixture(t, operation != "BindMission")
				release := lockObservationJournal(t, b)
				synctest.Test(t, func(t *testing.T) {
					defer release()
					ctx, cancel := context.WithCancelCause(context.Background())
					defer cancel(nil)
					cause := context.Canceled
					if mode == "cause" {
						cause = errors.New("parent observer cause")
					}
					if mode == "artifact cause" {
						cause = batuta.ErrArtifactInvalid
					}
					if mode == "deadline" {
						var stop context.CancelFunc
						ctx, stop = context.WithTimeout(ctx, time.Second)
						defer stop()
						cause = context.DeadlineExceeded
					}
					done := make(chan error, 1)
					go func() {
						var err error
						if operation == "BindMission" {
							_, err = svc.BindMissionContext(ctx, "m", MissionBindingRequest{WorkspaceID: "w", PlanPath: b.PlanPath, DeliveryID: b.DeliveryID})
						} else {
							var view domain.WatchView
							if operation == "Watch" {
								view, err = svc.WatchContext(ctx, "w", b.DeliveryID)
							} else {
								view, err = svc.WatchMissionContext(ctx, "m", "w", b.DeliveryID)
							}
							if view.Available || view.Progression != nil {
								t.Error("failed observer exposed snapshot", view)
							}
						}
						done <- err
					}()
					requireJournalWait(t, operation+"Context")
					if mode == "deadline" {
						<-ctx.Done()
					} else {
						cancel(cause)
					}
					synctest.Wait()
					var err error
					select {
					case err = <-done:
					default:
						t.Error("parent did not release held-lock observer")
						release()
						err = <-done
					}
					if !errors.Is(err, cause) || errors.Is(err, batuta.ErrArtifactIO) {
						t.Errorf("parent identity=%v, want %v", err, cause)
					}
				})
				lockObservationJournal(t, b)()
				if operation == "BindMission" {
					m, err := svc.Store.GetMission("m")
					if err != nil || m.Binding == nil || m.Binding.DeliveryID != "" {
						t.Error("canceled bind changed mission", m, err)
					}
				}
			})
		}
	}
}

func TestServiceJournalPostCaptureCatalogLifetime(t *testing.T) {
	for _, mode := range []string{"cancel", "cause", "deadline", "budget"} {
		t.Run(mode, func(t *testing.T) {
			svc, a, b := cancellationJournalFixture(t, false)
			synctest.Test(t, func(t *testing.T) {
				held, release := make(chan struct{}), make(chan struct{})
				holder := make(chan error, 1)
				go func() {
					_, err := svc.Store.ApprovePlan(context.Background(), a.Identity, func() ([]byte, error) {
						close(held)
						<-release
						return a.Content, nil
					})
					holder <- err
				}()
				<-held
				defer func() {
					close(release)
					if err := <-holder; err != nil {
						t.Error("catalog owner cleanup:", err)
					}
				}()
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				cause := context.Canceled
				if mode == "cause" {
					cause = errors.New("parent stopped receipt observation")
				}
				if mode == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, time.Second)
					defer stop()
					cause = context.DeadlineExceeded
				}
				done := make(chan error, 1)
				go func() {
					view, err := svc.projectMission(ctx, "m", "w", b)
					if view.Available || view.Progression != nil {
						t.Error("failed receipt observation exposed projection", view)
					}
					done <- err
				}()
				synctest.Wait()
				stack := make([]byte, 1<<20)
				stack = stack[:runtime.Stack(stack, true)]
				waiting := false
				for _, g := range strings.Split(string(stack), "\n\n") {
					if strings.Contains(g, "database/sql.(*DB).conn") && strings.Contains(g, "(*Service).projectMission") {
						waiting = true
						break
					}
				}
				if !waiting {
					t.Fatalf("observer did not finish capture and wait for catalog connection:\n%s", stack)
				}
				switch mode {
				case "deadline":
					<-ctx.Done()
				case "budget":
					cause = batuta.ErrArtifactIO
				default:
					cancel(cause)
				}
				var err error
				select {
				case err = <-done:
				case <-time.After(6 * time.Second):
					t.Fatal("post-capture catalog observation unbounded")
				}
				if !errors.Is(err, cause) || errors.Is(err, batuta.ErrArtifactInvalid) {
					t.Errorf("receipt observation lost cause or fabricated invalid artifact: %v; want %v", err, cause)
				}
				if mode == "budget" && errors.Is(err, context.DeadlineExceeded) {
					t.Error("catalog budget impersonated client deadline", err)
				}
			})
		})
	}
}
