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
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

func TestNativeExecutionRequiresApprovalBeforeMutation(t *testing.T) {
	h, svc, path := planHTTPFixture(t)
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"target": detail.Identity, "approval_id": ""})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v0/missions/m/execution", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("unapproved execution status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Effect string `json:"effect"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Effect != "rejected" || result.Error.Code != "execution_approval_required" {
		t.Fatalf("unapproved result=%+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unapproved request changed plan bytes", err)
	}
	if _, err := os.Stat(filepath.Join(detail.Identity.Binding.CanonicalRoot, ".batuta", "journal")); !os.IsNotExist(err) {
		t.Fatalf("unapproved request created native journal: %v", err)
	}
	m, err := svc.Store.GetMission("m")
	if err != nil || m.Binding == nil || m.Binding.DeliveryID != "" {
		t.Fatal("unapproved request changed delivery binding", err)
	}
}

func TestNativeExecutionHTTPStateAndRejectedStart(t *testing.T) {
	h, svc, path := planHTTPFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	c := client.New(server.URL, "secret")
	ctx := context.Background()
	detail, err := c.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := c.ApprovePlan(ctx, detail.Identity)
	if err != nil || approved.Receipt == nil {
		t.Fatal(approved, err)
	}
	state, err := c.ExecutionState(ctx, "m", "w", approved.Receipt.ID)
	if err != nil || state.Target != detail.Identity || state.Active || state.Bound || state.OpenedDigest != "" || state.Watch != nil {
		t.Fatal("unstarted native state", state, err)
	}
	result, err := c.StartExecution(ctx, domain.ExecutionRequest{Target: detail.Identity, ApprovalID: approved.Receipt.ID})
	var typed *client.ExecutionError
	if !errors.As(err, &typed) || typed.Status != 503 || result.Effect != domain.ExecutionRejected || result.Error == nil || result.Error.Code != "execution_unavailable" {
		t.Fatal("unavailable native start", result, err)
	}
	resumed, err := c.ResumeExecution(ctx, domain.ResumeRequest{Target: detail.Identity, ApprovalID: approved.Receipt.ID, Delivery: state.Delivery, OpenedDigest: "sha256:" + strings.Repeat("a", 64)})
	if !errors.As(err, &typed) || typed.Status != 400 || resumed.Effect != domain.ExecutionRejected {
		t.Fatal("unstarted native resume", resumed, err)
	}
	if _, err := os.Stat(filepath.Join(detail.Identity.Binding.CanonicalRoot, ".batuta/journal")); !os.IsNotExist(err) {
		t.Fatal("rejected native HTTP journey created journal", err)
	}
	if body, err := os.ReadFile(path); err != nil || domain.PlanVersion(body) != detail.Identity.Version {
		t.Fatal("rejected native HTTP journey changed source", err)
	}
	m, err := svc.Store.GetMission("m")
	if err != nil || m.Binding.DeliveryID != "" {
		t.Fatal(m, err)
	}
}
