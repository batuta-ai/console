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

func clientPlanIdentity() domain.PlanIdentity {
	return domain.PlanIdentity{MissionID: "m", Binding: domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: "/root", CanonicalRoot: "/root", PlanPath: ".batuta/plans/a.md", PlanSlug: "a", PlanDigest: "core"}, Version: domain.PlanVersion([]byte("shown"))}
}
func TestPlanClientTransportEffects(t *testing.T) {
	id := clientPlanIdentity()
	for _, mode := range []string{"lost", "canceled before", "canceled during", "redirect", "malformed", "wrong identity", "wrong bytes", "unauthorized", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c := New("http://console.test", "secret")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled before" {
				cancel()
			}
			c.HTTP.Transport = questionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "Bearer secret" || r.URL.Path != "/v0/missions/m/plan/approvals" {
					t.Fatal("wrong transport")
				}
				if mode == "lost" {
					return nil, io.ErrUnexpectedEOF
				}
				if mode == "canceled during" {
					cancel()
					return nil, r.Context().Err()
				}
				status := 200
				header := make(http.Header)
				receipt := domain.PlanApproval{ID: "receipt", Identity: id, Content: []byte("shown"), ApprovedAt: domain.Now()}
				result := domain.PlanApprovalResult{Target: id, Effect: domain.PlanApprovalRecorded, Receipt: &receipt}
				switch mode {
				case "redirect":
					status = 307
					header.Set("Location", "http://console.test/replayed")
				case "wrong identity":
					result.Target.MissionID = "other"
				case "wrong bytes":
					receipt.Content = []byte("other")
				case "unauthorized":
					status = 401
				case "rejected":
					status = 409
					result.Effect = domain.PlanApprovalRejected
					result.Receipt = nil
					result.Error = &domain.PlanFailure{Code: "plan_stale", Message: "changed"}
				}
				body, _ := json.Marshal(result)
				if mode == "malformed" {
					body = []byte(`{`)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			result, err := c.ApprovePlan(ctx, id)
			var typed *PlanError
			if !errors.As(err, &typed) {
				t.Fatal("untyped error", err)
			}
			expected := domain.PlanApprovalUncertain
			expectedCalls := 1
			if mode == "unauthorized" || mode == "rejected" || mode == "canceled before" {
				expected = domain.PlanApprovalRejected
			}
			if mode == "canceled before" {
				expectedCalls = 0
			}
			if result.Effect != expected || calls != expectedCalls {
				t.Fatalf("effect %s calls %d", result.Effect, calls)
			}
		})
	}
}
