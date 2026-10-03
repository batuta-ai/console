package api

import (
	"database/sql"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
)

func TestSessionGETCommitsRecoveryEvent(t *testing.T) {
	h, svc := testServer(t)
	if err := svc.Store.PutSession(domain.Session{ID: "old", Process: "running", Owner: "web", OwnerClient: "old-client"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req := httptest.NewRequest("GET", "/v0/sessions", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("GET: %d %s", w.Code, w.Body.String())
		}
	}
	got, err := svc.Store.GetSession("old")
	if err != nil || got.Process != "unknown" || got.Owner != "none" || got.OwnerClient != "" || got.EndedAt != nil {
		t.Errorf("GET retained obsolete control: %+v %v", got, err)
	}
	events, err := svc.Store.EventsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Kind == "session.reconciled" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want one atomic recovery event, got %+v", events)
	}
}

func TestRecoveryEventFailureReturnsHTTPErrorAndRetries(t *testing.T) {
	for _, process := range []string{"running", "unknown"} {
		t.Run(process, func(t *testing.T) {
			testRecoveryEventFailureReturnsHTTPErrorAndRetries(t, process)
		})
	}
}

func testRecoveryEventFailureReturnsHTTPErrorAndRetries(t *testing.T, process string) {
	t.Helper()
	cmd := exec.Command("/bin/cat")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	path := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := service.New(st, session.New(t.TempDir()), nil)
	h := (&Server{Svc: svc, Token: "test-token"}).Handler()
	original := domain.Session{ID: "old", Process: process, Owner: "web", OwnerClient: "old", PID: cmd.Process.Pid, Argv: []string{"/bin/cat"}, CreatedAt: domain.Now()}
	if err := st.PutSession(original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_recovery BEFORE INSERT ON events WHEN NEW.kind='session.reconciled' BEGIN SELECT RAISE(ABORT,'recovery rejected'); END`); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/v0/sessions/old/stop")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "stop_failed") || !strings.Contains(w.Body.String(), "recovery rejected") {
		t.Errorf("stop persistence failure must be HTTP 500/stop_failed: %d %s", w.Code, w.Body.String())
	}
	w = request("POST", "/v0/sessions/absent/stop")
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not_found") {
		t.Errorf("missing session response: %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/v0/sessions")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "recovery rejected") {
		t.Errorf("GET failure not observable: %d %s", w.Code, w.Body.String())
	}
	if err := svc.ReconcileSessions(); err == nil || !strings.Contains(err.Error(), "recovery rejected") {
		t.Errorf("startup recovery failure not observable: %v", err)
	}
	got, err := st.GetSession("old")
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("failed recovery changed row: %+v %v", got, err)
	}
	events, err := st.EventsSince(0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed event persisted: %+v %v", events, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_recovery"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := svc.ReconcileSessions(); err != nil {
			t.Fatal(err)
		}
		w = request("GET", "/v0/sessions")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	w = request("POST", "/v0/sessions/old/stop")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "session_not_owned") {
		t.Fatalf("lost ownership response: %d %s", w.Code, w.Body.String())
	}
	got, err = st.GetSession("old")
	want := original
	want.Process, want.Owner, want.OwnerClient, want.RecoveryReason = "unknown", "none", "", "identity_unverified"
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("stop falsified recovered row: %+v %v", got, err)
	}
	events, err = st.EventsSince(0, 100)
	if err != nil || len(events) != 1 || events[0].Kind != "session.reconciled" {
		t.Fatalf("retry events: %+v %v", events, err)
	}
	if !reflect.DeepEqual(events[0].Payload, map[string]any{"id": original.ID, "recovery_reason": "identity_unverified"}) {
		t.Fatalf("recovery payload: %+v", events[0])
	}
	if _, err := input.Write([]byte("still alive\n")); err != nil {
		t.Fatal("former process changed: ", err)
	}
}
