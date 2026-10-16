package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestStopHTTPContextHeldExit(t *testing.T) {
	for _, phase := range []string{"exit", "catalog"} {
		t.Run(phase, func(t *testing.T) { testStopHTTPContextPersistence(t, phase) })
	}
}

func TestStopHTTPContextAlreadyCanceled(t *testing.T) {
	h, svc := testServer(t)
	t.Cleanup(func() { svc.Session.Shutdown(context.Background()) })
	live, err := svc.Session.Start(domain.Session{ID: "http-canceled", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSession(live); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("request canceled before handler")
	cancel(cause)
	r := httptest.NewRequest(http.MethodPost, "/v0/sessions/http-canceled/stop", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 500 || !strings.Contains(w.Body.String(), cause.Error()) || !strings.Contains(w.Body.String(), `"error":"stop_failed"`) {
		t.Fatalf("fresh canceled response: %d %s", w.Code, w.Body.String())
	}
	if row, ok := svc.Session.Snapshot(live.ID); !ok || row.Process != "running" {
		t.Fatal("canceled HTTP stop terminated process")
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("canceled HTTP stop mutated catalog: %+v %v", events, err)
	}
	post(t, h, "/v0/sessions/http-canceled/stop", nil, 200)
}

type stopHTTPBarrier struct{ entered, release chan struct{} }

func (b stopHTTPBarrier) MarshalJSON() ([]byte, error) {
	close(b.entered)
	<-b.release
	return []byte(`true`), nil
}

func testStopHTTPContextPersistence(t *testing.T, phase string) {
	h, svc := testServer(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	barrierDone := make(chan error, 1)
	onExit := svc.Session.OnExit
	svc.Session.OnExit = func(row domain.Session) error {
		if phase == "exit" {
			close(entered)
			<-release
			return onExit(row)
		}
		if err := onExit(row); err != nil {
			return err
		}
		go func() {
			barrierDone <- svc.Store.PutWorkspaceWithEvent(domain.Workspace{ID: "http-barrier"}, "barrier", map[string]any{"hold": stopHTTPBarrier{entered, release}})
		}()
		<-entered
		return nil
	}
	t.Cleanup(func() { unblock(); svc.Session.Shutdown(context.Background()) })
	live, err := svc.Session.Start(domain.Session{ID: "http-stop", Argv: testfixture.Argv(t, []string{"/bin/cat"})}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSession(live); err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v0/sessions/http-stop/stop", nil))
	if unauthorized.Code != 401 {
		t.Fatalf("stop auth changed: %d", unauthorized.Code)
	}
	if row, ok := svc.Session.Snapshot(live.ID); !ok || row.Process != "running" {
		t.Fatal("unauthorized stop mutated process")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v0/sessions/http-stop/stop", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, r); close(done) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("native exit watchdog")
	}
	if phase == "catalog" {
		if _, err := svc.Session.Stop(live.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case <-done:
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"error":"stop_pending"`) {
			t.Fatalf("held persistence must return explicit non-success: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Error("HTTP stop ignored request cancellation while required exit persistence was held")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP completion watchdog")
	}
	if _, err := svc.StopSession(live.ID); err != nil {
		t.Fatal(err)
	}
	if phase == "catalog" {
		if err := <-barrierDone; err != nil {
			t.Fatal(err)
		}
	}
	events, err := svc.Store.EventsSince(0, 100)
	var kinds []string
	for _, e := range events {
		if e.Kind != "barrier" {
			kinds = append(kinds, e.Kind)
		}
	}
	if err != nil || len(kinds) != 2 || kinds[0] != "session.exited" || kinds[1] != "session.stopped" {
		t.Fatalf("durable completion: %+v %v", events, err)
	}
}
