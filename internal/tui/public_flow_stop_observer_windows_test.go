//go:build windows

package tui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/domain"
)

const winFlowStopBodyLimit = 16 << 10
const winFlowStopRecordLimit = 32

type winFlowStopObservation struct {
	Sequence   int                          `json:"sequence"`
	Path       string                       `json:"path"`
	Request    *domain.ExecutionStopRequest `json:"request,omitempty"`
	Status     int                          `json:"status"`
	BodyBytes  int                          `json:"body_bytes"`
	BodySHA256 string                       `json:"body_sha256"`
	Truncated  bool                         `json:"truncated"`
}

type winFlowStopSnapshot struct {
	Enabled  bool                     `json:"enabled"`
	Count    int                      `json:"count"`
	InFlight int                      `json:"in_flight"`
	Overflow bool                     `json:"overflow"`
	Records  []winFlowStopObservation `json:"records"`
}

type winFlowStopObserver struct {
	mu    sync.Mutex
	state winFlowStopSnapshot
}

func (o *winFlowStopObserver) snapshot() winFlowStopSnapshot {
	if o == nil {
		return winFlowStopSnapshot{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.state
	s.Records = append([]winFlowStopObservation(nil), s.Records...)
	for i := range s.Records {
		if s.Records[i].Request != nil {
			request := *s.Records[i].Request
			s.Records[i].Request = &request
		}
	}
	return s
}

type winFlowStopBody struct {
	io.ReadCloser
	captured []byte
	consumed int
}

func (b *winFlowStopBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.consumed += n
	remaining := winFlowStopBodyLimit - len(b.captured)
	b.captured = append(b.captured, p[:min(n, remaining)]...)
	return n, err
}

type winFlowStopWriter struct {
	http.ResponseWriter
	status int
}

// Only the JSON Stop route is wrapped. All other routes (including streaming
// and upgrades) retain their original writer; ResponseController can unwrap it.
func (w *winFlowStopWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *winFlowStopWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *winFlowStopWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func winFlowObserveStops(next http.Handler, enabled bool) (http.Handler, *winFlowStopObserver) {
	if !enabled {
		return next, nil
	}
	o := &winFlowStopObserver{state: winFlowStopSnapshot{Enabled: true}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if r.Method != http.MethodPost || len(parts) != 6 || parts[1] != "v0" || parts[2] != "missions" || parts[3] == "" || parts[4] != "execution" || parts[5] != "stop" {
			next.ServeHTTP(w, r)
			return
		}
		o.mu.Lock()
		o.state.Count++
		sequence := o.state.Count
		o.state.InFlight++
		o.mu.Unlock()
		body := &winFlowStopBody{ReadCloser: r.Body}
		r.Body = body
		writer := &winFlowStopWriter{ResponseWriter: w}
		// Tee only bytes requested by the original handler, never pre-read a body or
		// buffer a response. Do not retain headers or arbitrary request body text.
		defer func() {
			sum := sha256.Sum256(body.captured)
			record := winFlowStopObservation{Sequence: sequence, Path: r.URL.Path, Status: writer.status, BodyBytes: body.consumed, BodySHA256: hex.EncodeToString(sum[:]), Truncated: body.consumed > winFlowStopBodyLimit}
			var request domain.ExecutionStopRequest
			decoder := json.NewDecoder(bytes.NewReader(body.captured))
			decoder.DisallowUnknownFields()
			if !record.Truncated && decoder.Decode(&request) == nil && decoder.Decode(new(json.RawMessage)) == io.EOF && request.Validate() == nil {
				record.Request = &request
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			o.state.InFlight--
			if len(o.state.Records) < winFlowStopRecordLimit {
				o.state.Records = append(o.state.Records, record)
			} else {
				o.state.Overflow = true
			}
		}()
		next.ServeHTTP(writer, r)
	}), o
}

func TestWindowsOuterNativeStopObserverBoundary(t *testing.T) {
	original := (&api.Server{Token: "private-observer-fixture"}).Handler()
	disabled, absent := winFlowObserveStops(original, false)
	if absent != nil || reflect.ValueOf(disabled).Pointer() != reflect.ValueOf(original).Pointer() || absent.snapshot().Enabled {
		t.Fatal("default-off observer changed original handler")
	}
	serve := func(handler http.Handler, method, path, body string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorized {
			req.Header.Set("Authorization", "Bearer private-observer-fixture")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	path := "/v0/missions/observer/execution/stop"
	for _, tc := range []struct {
		name, body string
		authorized bool
		status     int
	}{
		{"auth-does-not-consume-body", `{"secret":"must not be retained"}`, false, 401},
		{"exact-original-rejection", `{}`, true, 400},
		{"malformed-body", `{`, true, 400},
		{"trailing-body", `{} {}`, true, 400},
		{"unknown-body-not-disclosed", `{"secret":"must not be retained"}`, true, 400},
		{"bounded-body", `{"secret":"` + strings.Repeat("x", winFlowStopBodyLimit*2) + `"}`, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, observer := winFlowObserveStops(original, true)
			baseline := serve(disabled, "POST", path, tc.body, tc.authorized)
			observed := serve(handler, "POST", path, tc.body, tc.authorized)
			if baseline.Code != tc.status || observed.Code != baseline.Code || !reflect.DeepEqual(observed.Header(), baseline.Header()) || observed.Body.String() != baseline.Body.String() {
				t.Fatal("observer altered original API response", observed, baseline)
			}
			snapshot := observer.snapshot()
			if !snapshot.Enabled || snapshot.Count != 1 || snapshot.InFlight != 0 || snapshot.Overflow || len(snapshot.Records) != 1 {
				t.Fatal("handler completion/count not observed", snapshot)
			}
			record := snapshot.Records[0]
			if record.Sequence != 1 || record.Path != path || record.Status != tc.status || record.Request != nil {
				t.Fatal("unexpected body/status record", record)
			}
			if !tc.authorized && record.BodyBytes != 0 {
				t.Fatal("observer eagerly consumed unauthenticated body", record)
			}
			if tc.name == "bounded-body" && (!record.Truncated || record.BodyBytes != winFlowStopBodyLimit+1) {
				t.Fatal("original MaxBytesReader limit not preserved", record)
			}
			raw := string(winFlowJSON(t, snapshot))
			if strings.Contains(raw, "secret") || strings.Contains(raw, "private-observer-fixture") {
				t.Fatal("observer disclosed arbitrary body or auth")
			}
			serve(handler, "GET", "/v0/capabilities", "", true)
			if !reflect.DeepEqual(snapshot, observer.snapshot()) {
				t.Fatal("non-Stop route was observed")
			}
		})
	}
	// A well-formed reference with a different route mission is rejected by the
	// real API before Service access, while retaining the exact consumed body.
	identity := domain.PlanIdentity{MissionID: "different-mission", Version: domain.PlanVersion([]byte("original source")), Binding: domain.MissionBinding{WorkspaceID: "workspace", WorkspaceRoot: "/owned", CanonicalRoot: "/owned", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "task-digest"}}
	request := domain.ExecutionStopRequest{Target: identity, ApprovalID: "receipt", Delivery: domain.ExecutionDelivery("receipt", identity), RunID: strings.Repeat("a", 64)}
	winFlowCheck(t, request.Validate())
	payload := string(winFlowJSON(t, request))
	typedHandler, typedObserver := winFlowObserveStops(original, true)
	res := serve(typedHandler, "POST", path, payload, true)
	typed := typedObserver.snapshot().Records[0]
	if res.Code != 400 || typed.Status != res.Code || typed.Request == nil || *typed.Request != request || typed.BodyBytes != len(payload) || typed.BodySHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(payload))) || typed.Truncated {
		t.Fatal("consumed reference/body digest differs at original API boundary", typed)
	}
	copy := typedObserver.snapshot()
	copy.Records[0].Request.RunID = strings.Repeat("b", 64)
	if *typedObserver.snapshot().Records[0].Request != request {
		t.Fatal("typed snapshot aliases retained request")
	}
	handler, observer := winFlowObserveStops(original, true)
	for i := 0; i < winFlowStopRecordLimit+1; i++ {
		serve(handler, "POST", path, `{}`, true)
	}
	snapshot := observer.snapshot()
	if snapshot.Count != winFlowStopRecordLimit+1 || snapshot.InFlight != 0 || !snapshot.Overflow || len(snapshot.Records) != winFlowStopRecordLimit {
		t.Fatal("observer record bound not enforced", snapshot)
	}
	writer := &winFlowStopWriter{ResponseWriter: httptest.NewRecorder()}
	winFlowCheck(t, http.NewResponseController(writer).Flush())
	if writer.Unwrap() == nil {
		t.Fatal("response controller cannot reach original writer")
	}
}

// This local wrapper control only qualifies accounting while a handler is in
// flight. Positive native outcomes always come from the actual API/Service.
func TestWindowsOuterNativeStopObserverInFlight(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("X-Original", "unchanged")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("original response"))
	})
	handler, observer := winFlowObserveStops(original, true)
	res := httptest.NewRecorder()
	go func() {
		defer close(done)
		handler.ServeHTTP(res, httptest.NewRequest("POST", "/v0/missions/owned/execution/stop", strings.NewReader(`{"secret":"unconsumed"}`)))
	}()
	<-entered
	before := observer.snapshot()
	close(release)
	<-done
	after := observer.snapshot()
	if before.Count != 1 || before.InFlight != 1 || len(before.Records) != 0 || before.Overflow || after.Count != 1 || after.InFlight != 0 || len(after.Records) != 1 || after.Records[0].BodyBytes != 0 || after.Records[0].Request != nil || after.Records[0].Status != 202 || res.Code != 202 || res.Header().Get("X-Original") != "unchanged" || res.Body.String() != "original response" {
		t.Fatal("inflight/completed accounting or lazy consumption/response changed", before, after, res)
	}
	after.Records[0].Status = 999
	if observer.snapshot().Records[0].Status != 202 {
		t.Fatal("snapshot aliases observer storage")
	}
}
