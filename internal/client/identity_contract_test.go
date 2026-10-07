package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestExactIdentityAcrossNamedAndAuthorityMethods(t *testing.T)     { testIdentityChain(t, false) }
func TestPureExactIdentityAcrossNamedAndAuthorityMethods(t *testing.T) { testIdentityChain(t, true) }
func testIdentityChain(t *testing.T, pure bool) {
	content := []byte{'#', ' ', 'x', '\r', '\n', 0xff, 0, '\n'}
	id := clientPlanIdentity()
	id.MissionID = "selected-2"
	id.Version = domain.PlanVersion(content)
	receipt := domain.PlanApproval{ID: "receipt-2", Identity: id, Content: content, ApprovedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)}
	delivery := domain.ExecutionDelivery(receipt.ID, id)
	opened := "sha256:" + domain.PlanVersion([]byte("exact-opened"))
	question := domain.QuestionIdentity{MissionID: id.MissionID, WorkspaceID: id.Binding.WorkspaceID, Delivery: delivery, OpenedDigest: opened, Task: "Task27", Execution: 3, QuestionID: "question-2"}
	gate := domain.ExecutionGate{Required: true, Delivery: delivery}
	calls := 0
	mutations := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing bearer")
		}
		if r.Method != "GET" {
			mutations++
		}
		decode := func(want any) {
			t.Helper()
			got := reflect.New(reflect.TypeOf(want))
			if err := json.NewDecoder(r.Body).Decode(got.Interface()); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got.Elem().Interface(), want) {
				t.Errorf("identity body %#v want %#v", got.Elem().Interface(), want)
			}
		}
		var result any
		switch r.URL.Path {
		case "/v0/missions":
			result = []domain.Mission{{ID: "other"}, {ID: id.MissionID, WorkspaceID: id.Binding.WorkspaceID, Binding: &id.Binding}}
		case "/v0/missions/selected-2/plan":
			if r.URL.Query().Get("workspace_id") != id.Binding.WorkspaceID {
				t.Error("wrong workspace")
			}
			result = domain.PlanDetail{Identity: id, Content: content}
		case "/v0/missions/selected-2/plan/approvals":
			decode(id)
			result = domain.PlanApprovalResult{Target: id, Effect: domain.PlanApprovalRecorded, Receipt: &receipt}
		case "/v0/missions/selected-2/plan/approvals/receipt-2":
			result = receipt
		case "/v0/missions/selected-2/execution":
			if r.Method == "POST" {
				w.WriteHeader(202)
				decode(domain.ExecutionRequest{Target: id, ApprovalID: receipt.ID})
				result = domain.ExecutionResult{Target: id, ApprovalID: receipt.ID, Delivery: delivery, OpenedDigest: opened, Effect: domain.ExecutionStarted}
			} else {
				if r.URL.Query().Get("approval_id") != receipt.ID {
					t.Error("receipt changed")
				}
				result = domain.ExecutionState{Target: id, ApprovalID: receipt.ID, Delivery: delivery, OpenedDigest: opened, Bound: true, Gate: &gate, Watch: &domain.WatchView{MissionID: id.MissionID, WorkspaceID: id.Binding.WorkspaceID, Delivery: delivery, Source: domain.SourceCurrent, Progression: &gate}}
			}
		case "/v0/missions/selected-2/execution/resume":
			w.WriteHeader(202)
			decode(domain.ResumeRequest{Target: id, ApprovalID: receipt.ID, Delivery: delivery, OpenedDigest: opened})
			result = domain.ExecutionResult{Target: id, ApprovalID: receipt.ID, Delivery: delivery, OpenedDigest: opened, Effect: domain.ExecutionStarted}
		case "/v0/watch/answer":
			decode(question.Answer("exact answer"))
			w.WriteHeader(500)
			result = domain.AnswerResult{Target: question, Delivery: delivery, Effect: domain.AnswerRecorded, Error: &domain.AnswerFailure{Code: "after_record", Message: "recorded before failure"}}
		default:
			t.Errorf("unexpected follow-up %s", r.URL)
			w.WriteHeader(404)
			return
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	})
	c := New("http://fixture", "secret")
	if pure {
		c.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			return rec.Result(), nil
		})
	} else {
		s := httptest.NewServer(handler)
		defer s.Close()
		c = New(s.URL, "secret")
	}
	ctx := context.Background()
	catalog, err := c.ListMissions(ctx, id.Binding.WorkspaceID)
	if err != nil || len(catalog) != 2 || catalog[1].ID != id.MissionID {
		t.Fatalf("catalog %v %v", catalog, err)
	}
	detail, err := c.PlanDetail(ctx, id.MissionID, id.Binding.WorkspaceID)
	if err != nil || !bytes.Equal(detail.Content, content) || detail.Identity != id {
		t.Fatalf("detail %+v %v", detail, err)
	}
	approved, err := c.ApprovePlan(ctx, detail.Identity)
	if err != nil || approved.Receipt == nil || !bytes.Equal(approved.Receipt.Content, content) {
		t.Fatalf("approval %+v %v", approved, err)
	}
	read, err := c.PlanApproval(ctx, id.MissionID, id.Binding.WorkspaceID, receipt.ID)
	if err != nil || read.ID != receipt.ID || !bytes.Equal(read.Content, content) {
		t.Fatalf("receipt %+v %v", read, err)
	}
	started, err := c.StartExecution(ctx, domain.ExecutionRequest{Target: id, ApprovalID: read.ID})
	if err != nil || started.Delivery != delivery {
		t.Fatalf("start %+v %v", started, err)
	}
	state, err := c.ExecutionState(ctx, id.MissionID, id.Binding.WorkspaceID, receipt.ID)
	if err != nil || state.OpenedDigest != opened {
		t.Fatalf("state %+v %v", state, err)
	}
	resumed, err := c.ResumeExecution(ctx, domain.ResumeRequest{Target: id, ApprovalID: receipt.ID, Delivery: state.Delivery, OpenedDigest: state.OpenedDigest})
	if err != nil || resumed.Delivery != delivery {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	answered, err := c.AnswerQuestion(ctx, question.Answer("exact answer"))
	var failure *AnswerError
	if !errors.As(err, &failure) || answered.Effect != domain.AnswerRecorded || answered.Target != question {
		t.Fatalf("recorded %+v %v", answered, err)
	}
	if calls != 8 || mutations != 4 {
		t.Fatalf("automatic mutation calls %d mutations %d", calls, mutations)
	}
}
