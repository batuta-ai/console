package api

import (
	"context"
	"encoding/json"
	"github.com/batuta-ai/console/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// This test deliberately uses only the existing HTTP surface, so its initial
// failure proves a missing route rather than missing production Go symbols.
func TestNativeStopHTTPRouteRequiresExactAuthority(t *testing.T) {
	_, svc, _ := planHTTPFixture(t)
	h := (&Server{Svc: svc, Token: "secret", Web: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("console")}}}).Handler()
	r := httptest.NewRequest(http.MethodPost, "/v0/missions/m/execution/stop", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("Stop HTTP status=%d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestNativeStopHTTPStrictBodyAndAuthentication(t *testing.T) {
	h, svc, _ := planHTTPFixture(t)
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	approval, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ExecutionStopRequest{Target: detail.Identity, ApprovalID: approval.ID, Delivery: domain.ExecutionDelivery(approval.ID, detail.Identity), RunID: strings.Repeat("a", 64)}
	payload, _ := json.Marshal(req)
	for _, scenario := range []string{"unknown_run", "unknown_field", "trailing", "query", "cross_mission", "missing_run", "wrong_delivery", "malformed", "unauthorized"} {
		t.Run(scenario, func(t *testing.T) {
			body, path, token := string(payload), "/v0/missions/m/execution/stop", "secret"
			want := 400
			switch scenario {
			case "unknown_field":
				body = strings.TrimSuffix(body, "}") + `,"unknown":true}`
			case "trailing":
				body += `{}`
			case "query":
				path += "?run_id=other"
			case "cross_mission":
				path = "/v0/missions/other/execution/stop"
			case "missing_run":
				bad := req
				bad.RunID = ""
				b, _ := json.Marshal(bad)
				body = string(b)
			case "wrong_delivery":
				bad := req
				bad.Delivery = "other"
				b, _ := json.Marshal(bad)
				body = string(b)
			case "malformed":
				body = `{`
			case "unauthorized":
				token = "wrong"
				want = 401
			}
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
			}
			if scenario != "unauthorized" {
				var result domain.ExecutionStopResult
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Effect != domain.ExecutionStopRejected || result.Finalized || result.Durable || result.Error == nil {
					t.Fatal(result, err)
				}
			}
		})
	}
}
