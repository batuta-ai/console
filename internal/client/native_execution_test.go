package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeClientPreservesExactTargetAndTransportUncertainty(t *testing.T) {
	identity := domain.PlanIdentity{MissionID: "m", Binding: domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/root", CanonicalRoot: "/root", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "tasks"}, Version: domain.PlanVersion([]byte("approved"))}
	req := domain.ExecutionRequest{Target: identity, ApprovalID: "receipt"}
	for _, scenario := range []string{"rejected", "lost", "foreign", "canceled", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			c := New("http://console.test", "secret")
			calls := 0
			c.HTTP.Transport = questionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v0/missions/m/execution" || r.Header.Get("Authorization") != "Bearer secret" || r.GetBody != nil {
					t.Fatal("unsafe native request", r)
				}
				var sent domain.ExecutionRequest
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil || sent != req {
					t.Fatal(sent, err)
				}
				if scenario == "lost" {
					return nil, io.ErrUnexpectedEOF
				}
				result := domain.ExecutionResult{Target: identity, ApprovalID: req.ApprovalID, Effect: domain.ExecutionRejected, Error: &domain.PlanFailure{Code: "execution_approval_required", Message: "approval required"}}
				if scenario == "foreign" {
					result.Target.MissionID = "other"
				}
				body, _ := json.Marshal(result)
				status := 409
				headers := http.Header{}
				if scenario == "redirect" {
					status = 307
					headers.Set("Location", "http://foreign.test/execution")
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			ctx := context.Background()
			if scenario == "canceled" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			result, err := c.StartExecution(ctx, req)
			var typed *ExecutionError
			if !errors.As(err, &typed) || result.Target != identity || result.ApprovalID != req.ApprovalID {
				t.Fatal(result, err)
			}
			expected := domain.ExecutionUncertain
			if scenario == "rejected" || scenario == "canceled" {
				expected = domain.ExecutionRejected
			}
			if result.Effect != expected {
				t.Fatal("incorrect native mutation effect", result, err)
			}
			if scenario == "canceled" {
				if calls != 0 {
					t.Fatal("canceled native mutation sent")
				}
			} else if calls != 1 {
				t.Fatal("native mutation replayed", calls)
			}
		})
	}
}

func TestNativeStateRejectsForeignOrUnclearedCompletionGate(t *testing.T) {
	identity := domain.PlanIdentity{MissionID: "m", Binding: domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/root", CanonicalRoot: "/root", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "tasks"}, Version: domain.PlanVersion([]byte("approved"))}
	delivery := domain.ExecutionDelivery("receipt", identity)
	for _, scenario := range []string{"valid", "foreign", "mismatched", "premature", "missing", "typed_error"} {
		t.Run(scenario, func(t *testing.T) {
			gate := &domain.ExecutionGate{Required: true, Delivery: delivery}
			watchGate := *gate
			state := domain.ExecutionState{Target: identity, ApprovalID: "receipt", Delivery: delivery, OpenedDigest: "sha256:" + strings.Repeat("a", 64), Bound: true, Gate: gate, Watch: &domain.WatchView{MissionID: "m", WorkspaceID: "w", Delivery: delivery, Source: domain.SourceCurrent, Header: map[string]any{"state": "review_blocked"}, Progression: &watchGate}}
			switch scenario {
			case "foreign":
				gate.Delivery = "foreign"
			case "mismatched":
				watchGate.Cleared = true
			case "premature":
				state.Watch.Header["state"] = "done"
			case "missing":
				state.Gate, state.Watch.Progression = nil, nil
			}
			c := New("http://console.test", "secret")
			c.HTTP.Transport = questionTransport(func(r *http.Request) (*http.Response, error) {
				body, _ := json.Marshal(state)
				status := 200
				if scenario == "typed_error" {
					body, status = []byte(`{"error":"execution_reference","message":"invalid reference"}`), 400
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
			})
			got, err := c.ExecutionState(context.Background(), "m", "w", "receipt")
			if scenario == "valid" {
				if err != nil || got.Gate == nil || got.Gate.Cleared {
					t.Fatal(got, err)
				}
			} else {
				var typed *ExecutionError
				if !errors.As(err, &typed) {
					t.Fatal("invalid native gate/query was trusted", scenario, got, err)
				}
			}
		})
	}
}
