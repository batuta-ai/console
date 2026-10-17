package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/batuta-ai/console/internal/domain"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestNativeStopClientValidatesIdentityEffectAndStatus(t *testing.T) {
	target := domain.PlanIdentity{MissionID: "m", Binding: domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/w", CanonicalRoot: "/w", PlanPath: ".batuta/plans/a.md", PlanSlug: "a", PlanDigest: "tasks"}, Version: domain.PlanVersion([]byte("approved"))}
	req := domain.ExecutionStopRequest{Target: target, ApprovalID: "receipt", Delivery: domain.ExecutionDelivery("receipt", target), RunID: strings.Repeat("a", 64)}
	for _, scenario := range []string{"pending", "completed", "uncertain", "rejected", "foreign_run", "foreign_mission", "wrong_status", "false_durable", "false_complete", "unknown", "trailing", "lost", "redirect", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			c := New("http://console.test", "secret")
			calls := 0
			c.HTTP.Transport = questionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v0/missions/m/execution/stop" || r.Header.Get("Authorization") != "Bearer secret" || r.GetBody != nil {
					t.Fatal("unsafe request", r)
				}
				var sent domain.ExecutionStopRequest
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil || sent != req {
					t.Fatal(sent, err)
				}
				out := domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopPending}
				status := 202
				switch scenario {
				case "completed":
					out.Effect = domain.ExecutionStopCompleted
					out.Finalized = true
					out.Durable = true
					out.State = &domain.ExecutionState{Target: target, ApprovalID: req.ApprovalID, Delivery: req.Delivery}
					status = 200
				case "uncertain":
					out.Effect = domain.ExecutionStopUncertain
					out.Finalized = true
					out.Error = &domain.PlanFailure{Code: "execution_stop_uncertain", Message: "journal failure"}
					status = 503
				case "rejected":
					out.Effect = domain.ExecutionStopRejected
					out.Error = &domain.PlanFailure{Code: "invalid_reference", Message: "old"}
					status = 400
				case "foreign_run":
					out.RunID = strings.Repeat("b", 64)
				case "foreign_mission":
					out.Target.MissionID = "other"
				case "wrong_status":
					status = 200
				case "false_durable":
					out.Durable = true
				case "false_complete":
					out.Effect = domain.ExecutionStopCompleted
					status = 200
				case "lost":
					return nil, io.ErrUnexpectedEOF
				case "redirect":
					status = 307
				}
				body, _ := json.Marshal(out)
				if scenario == "unknown" {
					body = append(body[:len(body)-1], []byte(`,"unknown":true}`)...)
				}
				if scenario == "trailing" {
					body = append(body, []byte(`{}`)...)
				}
				headers := http.Header{}
				if scenario == "redirect" {
					headers.Set("Location", "http://foreign.test/stop")
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			ctx := context.Background()
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := c.StopExecution(ctx, req)
			if got.ExecutionStopRequest != req {
				t.Fatal("lost requested identity", got, err)
			}
			if scenario == "pending" || scenario == "completed" {
				if err != nil {
					t.Fatal(got, err)
				}
			} else {
				var typed *ExecutionStopError
				if !errors.As(err, &typed) {
					t.Fatal("untrusted Stop response accepted", got, err)
				}
			}
			want := 1
			if scenario == "canceled" {
				want = 0
			}
			if calls != want {
				t.Fatal("request replay", calls)
			}
		})
	}
}

func TestNativeStopClientDoesNotSendMalformedAuthority(t *testing.T) {
	c := New("http://console.test", "secret")
	c.HTTP.Transport = questionTransport(func(*http.Request) (*http.Response, error) { t.Fatal("sent invalid authority"); return nil, nil })
	req := domain.ExecutionStopRequest{}
	got, err := c.StopExecution(context.Background(), req)
	if err == nil || !reflect.DeepEqual(got.ExecutionStopRequest, req) || got.Effect != domain.ExecutionStopRejected {
		t.Fatal(got, err)
	}
}

func TestNativeStopClientRejectsUnverifiedFinalStates(t *testing.T) {
	target := domain.PlanIdentity{MissionID: "m", Binding: domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/w", CanonicalRoot: "/w", PlanPath: ".batuta/plans/a.md", PlanSlug: "a", PlanDigest: "tasks"}, Version: domain.PlanVersion([]byte("approved"))}
	req := domain.ExecutionStopRequest{Target: target, ApprovalID: "receipt", Delivery: domain.ExecutionDelivery("receipt", target), RunID: strings.Repeat("a", 64)}
	for _, scenario := range []string{"canceled", "done", "unknown_core", "mismatched_terminal", "no_terminal", "pending_review"} {
		t.Run(scenario, func(t *testing.T) {
			gate := &domain.ExecutionGate{Required: true, Delivery: req.Delivery}
			state := &domain.ExecutionState{Target: target, ApprovalID: req.ApprovalID, Delivery: req.Delivery, OpenedDigest: "sha256:" + strings.Repeat("b", 64), Bound: true, Gate: gate}
			state.Watch = &domain.WatchView{MissionID: "m", WorkspaceID: "w", Delivery: req.Delivery, Source: domain.SourceCurrent, Progression: gate, Header: map[string]any{"state": "canceled"}}
			out := domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopCompleted, Finalized: true, Durable: true, CoreState: "canceled", State: state}
			switch scenario {
			case "done":
				out.CoreState, state.Watch.Header["state"] = "done", "done"
				gate.Cleared, gate.ReviewID, gate.EvidenceDigest = true, "review", "sha256:"+strings.Repeat("c", 64)
			case "unknown_core":
				out.CoreState = "invented"
			case "mismatched_terminal":
				state.Watch.Header["state"] = "blocked"
			case "no_terminal":
				state.Watch.Header["state"] = "running"
			case "pending_review":
				out.CoreState, state.Watch.Header["state"] = "done", "review_blocked"
			}
			c := New("http://console.test", "secret")
			c.HTTP.Transport = questionTransport(func(*http.Request) (*http.Response, error) {
				body, err := json.Marshal(out)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			_, err := c.StopExecution(context.Background(), req)
			if scenario == "canceled" || scenario == "done" {
				if err != nil {
					t.Fatal("verified final response rejected", err)
				}
			} else if err == nil {
				t.Fatal("unverified terminal accepted as completed", scenario)
			}
		})
	}
}
