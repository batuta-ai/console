package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func lockHTTPJournal(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := holdObservationJournal(f)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if err := unlock(); err != nil {
			t.Error(err)
		}
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}

func TestHTTPJournalObservationLifetime(t *testing.T) {
	for _, route := range []string{"bind", "mission", "watch", "plan", "state"} {
		for _, mode := range []string{"cancel", "deadline", "cause", "budget"} {
			if mode == "cause" && (route == "plan" || route == "state") {
				continue
			}
			t.Run(route+"/"+mode, func(t *testing.T) {
				h, svc, _ := planHTTPFixture(t)
				detail, err := svc.PlanDetail(context.Background(), "m", "w")
				if err != nil {
					t.Fatal(err)
				}
				a, err := svc.ApprovePlan(context.Background(), detail.Identity)
				if err != nil {
					t.Fatal(err)
				}
				b := a.Identity.Binding
				b.DeliveryID = batuta.ExecutionDelivery(a)
				js, err := journal.Open(b.CanonicalRoot)
				if err != nil {
					t.Fatal(err)
				}
				opening, _ := json.Marshal(map[string]string{"slug": b.PlanSlug, "plan_path": b.PlanPath, "plan_digest": b.PlanDigest, "workspace": b.CanonicalRoot, "approval_receipt_id": a.ID, "plan_content_digest": a.Identity.Version})
				record, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: opening, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
				if err != nil {
					t.Fatal(err)
				}
				b.OpenedDigest = record.Digest
				if route != "bind" {
					if err := svc.Store.BindMission("m", b); err != nil {
						t.Fatal(err)
					}
				}
				before, err := svc.Store.GetMission("m")
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(b.CanonicalRoot, journal.Dir, b.DeliveryID+".jsonl")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				release := lockHTTPJournal(t, path)
				method, url, body := http.MethodGet, "/v0/watch?workspace=w&delivery="+b.DeliveryID, ""
				switch route {
				case "bind":
					method, url = http.MethodPut, "/v0/missions/m/binding"
					data, _ := json.Marshal(map[string]string{"workspace_id": "w", "plan_path": b.PlanPath, "delivery_id": b.DeliveryID})
					body = string(data)
				case "mission":
					url = "/v0/missions/m/watch?workspace=w&delivery=" + b.DeliveryID
				case "plan":
					url = "/v0/missions/m/plan?workspace_id=w"
				case "state":
					url = "/v0/missions/m/execution?workspace_id=w&approval_id=" + a.ID
				}
				unauthorized := httptest.NewRecorder()
				h.ServeHTTP(unauthorized, httptest.NewRequest(method, url, strings.NewReader(body)))
				if unauthorized.Code != 401 {
					t.Fatalf("authentication status=%d", unauthorized.Code)
				}
				synctest.Test(t, func(t *testing.T) {
					defer release()
					ctx, cancel := context.WithCancelCause(context.Background())
					defer cancel(nil)
					if mode == "deadline" {
						var stop context.CancelFunc
						ctx, stop = context.WithTimeout(ctx, time.Second)
						defer stop()
					}
					req := httptest.NewRequest(method, url, strings.NewReader(body)).WithContext(ctx)
					req.Header.Set("Authorization", "Bearer secret")
					rec := httptest.NewRecorder()
					done := make(chan struct{})
					start := time.Now()
					go func() { defer close(done); h.ServeHTTP(rec, req) }()
					synctest.Wait()
					stack := make([]byte, 1<<20)
					stack = stack[:runtime.Stack(stack, true)]
					waiting := false
					for _, g := range strings.Split(string(stack), "\n\n") {
						if strings.Contains(g, "core/journal.lockJournalContext") && strings.Contains(g, "console/internal/api.") {
							waiting = true
							break
						}
					}
					if !waiting {
						t.Fatalf("HTTP request never reached real held journal lock:\n%s", stack)
					}
					switch mode {
					case "cancel":
						cancel(context.Canceled)
					case "cause":
						cancel(errors.New("client observation abandoned"))
					case "deadline":
						<-ctx.Done()
					}
					synctest.Wait()
					if mode == "budget" {
						select {
						case <-done:
						case <-time.After(6 * time.Second):
							t.Error("unbounded HTTP observation")
							release()
							<-done
						}
						if time.Since(start) != 5*time.Second {
							t.Errorf("observer budget=%v, want 5s", time.Since(start))
						}
					} else {
						select {
						case <-done:
						default:
							t.Error("request cancellation did not reach journal while writer held lock")
							release()
							<-done
						}
					}
					status, code := 408, "binding_canceled"
					if route == "plan" {
						code = "plan_canceled"
					}
					if route == "state" {
						code = "execution_canceled"
					}
					if mode == "budget" {
						status, code = 503, "binding_io"
						if route == "plan" || route == "state" {
							code = "plan_io"
						}
					}
					var wire struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
						t.Error(err)
					}
					if rec.Code != status || wire.Error != code {
						t.Errorf("response=%d %s; want %d %s", rec.Code, rec.Body.String(), status, code)
					}
				})
				lockHTTPJournal(t, path)()
				after, err := svc.Store.GetMission("m")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Error("failed observer mutated binding", after, err)
				}
				current, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(original, current) {
					t.Error("failed observer mutated journal", err)
				}
			})
		}
	}
}
