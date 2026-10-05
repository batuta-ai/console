package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/store"
)

func planHTTPFixture(t *testing.T) (http.Handler, *service.Service, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := service.New(st, nil, nil)
	root := t.TempDir()
	path := bindingFixture(t, root, "alpha", "")
	if err := st.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission("m", service.MissionBindingRequest{WorkspaceID: "w", PlanPath: path}); err != nil {
		t.Fatal(err)
	}
	return (&Server{Svc: svc, Token: "secret"}).Handler(), svc, filepath.Join(root, path)
}

type planHandlerTransport struct{ handler http.Handler }

func (p planHandlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func planHTTPJourney(t *testing.T, c *client.Client, svc *service.Service, path string) {
	t.Helper()
	ctx := context.Background()
	shown, err := c.PlanDetail(ctx, "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	if shown.Identity.MissionID != "m" || shown.Approval != nil {
		t.Fatal("incorrect detail")
	}
	result, err := c.ApprovePlan(ctx, shown.Identity)
	if err != nil || result.Receipt == nil || result.Effect != domain.PlanApprovalRecorded {
		t.Fatal("approve", result, err)
	}
	saved, err := c.PlanApproval(ctx, "m", "w", result.Receipt.ID)
	if err != nil || saved.Identity != shown.Identity || !bytes.Equal(saved.Content, shown.Content) {
		t.Fatal("reload", err)
	}
	if _, err := c.PlanApproval(ctx, "other", "w", saved.ID); err == nil {
		t.Fatal("foreign receipt")
	}
	detail, err := c.PlanDetail(ctx, "m", "w")
	if err != nil || detail.Approval == nil || detail.Approval.ID != saved.ID {
		t.Fatal("detail receipt", err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, shown.Content...), []byte("\n## Decisions and context\nEdited prose.\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	stale, err := c.ApprovePlan(ctx, shown.Identity)
	var typed *client.PlanError
	if !errors.As(err, &typed) || typed.Code != "plan_stale" || typed.Status != 409 || stale.Effect != domain.PlanApprovalRejected {
		t.Fatalf("stale %v %+v", err, stale)
	}
	historical, err := svc.PlanApproval(ctx, "m", "w", saved.ID)
	if err != nil || !bytes.Equal(historical.Content, shown.Content) {
		t.Fatal("immutable", err)
	}
}

func TestPlanHTTPJourneyInProcess(t *testing.T) {
	h, svc, path := planHTTPFixture(t)
	c := client.New("http://console.test", "secret")
	c.HTTP.Transport = planHandlerTransport{h}
	planHTTPJourney(t, c, svc, path)
}
func TestPlanHTTPJourneyNative(t *testing.T) {
	h, svc, path := planHTTPFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	planHTTPJourney(t, client.New(server.URL, "secret"), svc, path)
}

func TestPlanHTTPRejectsWithoutMutation(t *testing.T) {
	for _, mode := range []string{"unauthorized", "trailing", "unknown", "oversize", "absent", "foreign", "wrong route", "stale", "canceled", "root"} {
		t.Run(mode, func(t *testing.T) {
			h, svc, path := planHTTPFixture(t)
			shown, err := svc.PlanDetail(context.Background(), "m", "w")
			if err != nil {
				t.Fatal(err)
			}
			id := shown.Identity
			status := 400
			route := "/v0/missions/m/plan/approvals"
			switch mode {
			case "foreign":
				id.Binding.WorkspaceID = "other"
			case "wrong route":
				route = "/v0/missions/other/plan/approvals"
			case "absent":
				id = domain.PlanIdentity{}
			case "stale":
				id.Version = strings.Repeat("0", 64)
				status = 409
			case "root":
				id.Binding.WorkspaceID = id.Binding.WorkspaceRoot
			case "canceled":
				status = 408
			case "unauthorized":
				status = 401
			}
			body, _ := json.Marshal(id)
			switch mode {
			case "trailing":
				body = append(body, []byte(" {}")...)
			case "unknown":
				body = append(body[:len(body)-1], []byte(`,"unknown":true}`)...)
			case "oversize":
				body = []byte(`{"mission_id":"` + strings.Repeat("a", 16384) + `"}`)
			}
			r := httptest.NewRequest("POST", route, bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer secret")
			if mode == "unauthorized" {
				r.Header.Del("Authorization")
			}
			if mode == "canceled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != status {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			events, err := svc.Store.EventsSince(0, 100)
			if err != nil || len(events) != 1 {
				t.Fatal("mutation", events, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, shown.Content) {
				t.Fatal("rewrote source", err)
			}
		})
	}
}

// The server commits but transport loses its reply: no resend and a recoverable receipt.
func TestPlanHTTPCommittedReplyLost(t *testing.T) {
	h, svc, _ := planHTTPFixture(t)
	shown, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := client.New("http://console.test", "secret")
	c.HTTP.Transport = planLossTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		return nil, io.ErrUnexpectedEOF
	})
	result, err := c.ApprovePlan(context.Background(), shown.Identity)
	if err == nil || calls != 1 || result.Effect != domain.PlanApprovalUncertain {
		t.Fatal("untruthful loss", result, err)
	}
	detail, err := svc.PlanDetail(context.Background(), "m", "w")
	if err != nil || detail.Approval == nil {
		t.Fatal("lost durable receipt", err)
	}
}

type planLossTransport func(*http.Request) (*http.Response, error)

func (f planLossTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPlanHTTPDetailReferences(t *testing.T) {
	h, _, _ := planHTTPFixture(t)
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/v0/missions/m/plan?workspace_id=w", "", 401},
		{"/v0/missions/m/plan", "secret", 400},
		{"/v0/missions/m/plan?workspace_id=other", "secret", 400},
		{"/v0/missions/m/plan?workspace_id=w&root=/tmp", "secret", 400},
		{"/v0/missions/m/plan?workspace_id=w&workspace_id=w", "secret", 400},
		{"/v0/missions/m/plan?workspace_id=w&ignored=%zz", "secret", 400},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d want %d", tc.path, w.Code, tc.status)
		}
	}
}

func TestPlanHTTPProposedContentIsLossless(t *testing.T) {
	h, svc, path := planHTTPFixture(t)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.ReplaceAll(bytes.Replace(original, []byte("approved"), []byte("proposed"), 1), []byte("\n"), []byte("\r\n"))
	source = append(source, []byte("\r\n## Decisions and context\r\nByte \xff is retained.\r\n")...)
	if err := os.WriteFile(path, source, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	c := client.New("http://console.test", "secret")
	c.HTTP.Transport = planHandlerTransport{h}
	detail, err := c.PlanDetail(context.Background(), "m", "w")
	if err != nil || !bytes.Equal(detail.Content, source) || detail.Status != "proposed" {
		t.Fatal("lossless detail", err)
	}
	result, err := c.ApprovePlan(context.Background(), detail.Identity)
	if err != nil || result.Receipt == nil || !bytes.Equal(result.Receipt.Content, source) {
		t.Fatal("lossless receipt", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source, after) {
		t.Fatal("native source changed", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode() != before.Mode() {
		t.Fatal("source chmod", err)
	}
	saved, err := svc.PlanApproval(context.Background(), "m", "w", result.Receipt.ID)
	if err != nil || !bytes.Equal(saved.Content, source) {
		t.Fatal("lossless historical", err)
	}
}
