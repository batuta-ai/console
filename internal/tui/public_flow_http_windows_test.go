//go:build windows

package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"golang.org/x/sys/windows"
)

type winFlowHTTP struct{ *winFlowHost }

func winHTTPRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}

// A fresh connection and caller context for every observation also proves that
// accepted native work outlives the HTTP caller. No mutation is retried here.
func (h *winFlowHTTP) requestContext(t *testing.T, parent context.Context, method, path string, data []byte) (int, []byte, error) {
	t.Helper()
	if err := context.Cause(parent); err != nil {
		return 0, nil, err
	}
	ctx, cancel := winHTTPRequestContext(parent)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+h.address+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = nil
	audit := &winHTTPWire{t: t, base: transport, token: h.token}
	c := &http.Client{Transport: audit, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(req)
	if err != nil {
		return audit.status, audit.raw, err
	}
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, errors.Join(err, res.Body.Close(), context.Cause(ctx))
}

// Capture the real wire before the official public client decodes it.
type winHTTPWire struct {
	t      *testing.T
	base   http.RoundTripper
	token  string
	status int
	raw    []byte
}

func (w *winHTTPWire) RoundTrip(req *http.Request) (*http.Response, error) {
	auth := ""
	if w.token != "" {
		auth = "Bearer " + w.token
	}
	if req.Header.Get("Authorization") != auth || (req.ContentLength > 0 && req.Header.Get("Content-Type") != "application/json") {
		return nil, errors.New("public request authentication/content type drift")
	}
	res, err := w.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	w.status = res.StatusCode
	w.raw, err = io.ReadAll(io.LimitReader(res.Body, winFlowLimit+1))
	err = errors.Join(err, res.Body.Close(), context.Cause(req.Context()))
	w.t.Logf("HTTP %s %s status=%d content-type=%q response=%s error=%v", req.Method, req.URL.Path, res.StatusCode, res.Header.Get("Content-Type"), w.raw, err)
	if err != nil {
		return nil, err
	}
	if len(w.raw) > winFlowLimit || res.Header.Get("Content-Type") != "application/json" {
		return nil, errors.New("public response bound/content type drift")
	}
	res.Body = io.NopCloser(bytes.NewReader(w.raw))
	return res, nil
}
func (h *winFlowHTTP) clientCall(t *testing.T, parent context.Context, call func(context.Context, *client.Client) error) (int, []byte, error) {
	t.Helper()
	if err := context.Cause(parent); err != nil {
		return 0, nil, err
	}
	ctx, cancel := winHTTPRequestContext(parent)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	wire := &winHTTPWire{t: t, base: transport, token: h.token}
	c := client.New("http://"+h.address, h.token)
	c.HTTP = &http.Client{Transport: wire, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	err := call(ctx, c)
	return wire.status, wire.raw, errors.Join(err, context.Cause(ctx))
}
func (h *winFlowHTTP) request(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	var data []byte
	if body != nil {
		data = winFlowJSON(t, body)
	}
	status, raw, err := h.requestContext(t, context.Background(), method, path, data)
	winFlowCheck(t, err)
	if out != nil {
		winFlowCheck(t, winFlowDecode(raw, out))
	}
	return status
}
func (h *winFlowHTTP) want(t *testing.T, method, path string, body, out any, status int) {
	t.Helper()
	if got := h.request(t, method, path, body, out); got != status {
		t.Fatalf("%s %s: status %d want %d", method, path, got, status)
	}
}
func winHTTPAwaitResult(ctx context.Context, what string, check func() (bool, error)) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := context.Cause(ctx); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		ready, err := check()
		if cause := context.Cause(ctx); cause != nil {
			return fmt.Errorf("%s: %w", what, cause)
		}
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, context.Cause(ctx))
		case <-ticker.C:
		}
	}
}

func TestWindowsPublicFlowHTTPAwaitCancellation(t *testing.T) {
	for _, mode := range []string{"already-canceled", "already-expired", "canceled-during-check", "expired-during-check"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			want := context.Canceled
			switch mode {
			case "already-canceled":
				cancel(want)
			case "already-expired":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Time{})
				defer stop()
				want = context.DeadlineExceeded
			case "expired-during-check":
				want = context.DeadlineExceeded
			}
			calls := 0
			err := winHTTPAwaitResult(ctx, mode, func() (bool, error) {
				calls++
				cancel(want)
				return true, nil
			})
			wantCalls := 1
			if strings.HasPrefix(mode, "already-") {
				wantCalls = 0
			}
			if !errors.Is(err, want) || calls != wantCalls {
				t.Errorf("late success/inspection: err=%v calls=%d want=%v/%d", err, calls, want, wantCalls)
			}
			err = winHTTPAwaitResult(ctx, "subsequent success cannot rescue expired wait", func() (bool, error) {
				calls++
				return true, nil
			})
			if !errors.Is(err, want) || calls != wantCalls {
				t.Errorf("expired wait rescued: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestWindowsPublicFlowHTTPRequestContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if state, err := (&winFlowHTTP{}).readiness(t, context.Background(), domain.PlanApproval{}); err == nil || state != nil {
			t.Fatal("unbounded readiness accepted", state, err)
		}
		parent, stop := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
		defer stop()
		ctx, cancel := winHTTPRequestContext(parent)
		defer cancel()
		parentDeadline, _ := parent.Deadline()
		deadline, ok := ctx.Deadline()
		if !ok || deadline != parentDeadline {
			t.Fatal("request inflated poll deadline", deadline, parentDeadline)
		}
		stop()
		if !errors.Is(context.Cause(ctx), context.Canceled) {
			t.Fatal("request did not inherit poll cancellation", context.Cause(ctx))
		}
		before := time.Now()
		capped, release := winHTTPRequestContext(context.Background())
		defer release()
		after := time.Now()
		deadline, ok = capped.Deadline()
		if !ok || deadline.Before(before.Add(10*time.Second)) || deadline.After(after.Add(10*time.Second)) {
			t.Fatal("request cap changed", deadline)
		}
	})
}

func winHTTPAccount(t *testing.T, h *winFlowHTTP) domain.Account {
	t.Helper()
	var account domain.Account
	h.want(t, "POST", "/v0/accounts", map[string]string{"label": "owned", "adapter": h.cfg.Provider}, &account, 200)
	if e := h.control(t, winFlowCommand{Op: "authenticate", AccountID: account.ID}); e.Kind != "authenticated" {
		t.Fatal(e)
	}
	h.want(t, "POST", "/v0/accounts/"+account.ID+"/connect", nil, &account, 200)
	if !account.Connected || account.SessionCap != 2 {
		t.Fatal("account not connected/capacity two", account)
	}
	return account
}
func winHTTPApprove(t *testing.T, h *winFlowHTTP, root string, account domain.Account) domain.PlanApproval {
	t.Helper()
	var ws domain.Workspace
	h.want(t, "POST", "/v0/workspaces", map[string]string{"name": "owned", "root": root}, &ws, 200)
	model := map[string]string{"codex": "qa-native", "claude": "sonnet"}[h.cfg.Provider]
	var squad domain.Squad
	h.want(t, "POST", "/v0/squads", domain.Squad{Name: "owned", Members: []domain.Member{{Role: "lead", CLI: h.cfg.Provider, Model: model, AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}}, &squad, 200)
	var mission domain.Mission
	h.want(t, "POST", "/v0/missions", map[string]string{"workspace_id": ws.ID, "title": "owned", "squad_id": squad.ID}, &mission, 200)
	base := "/v0/missions/" + mission.ID
	h.want(t, "PUT", base+"/binding", service.MissionBindingRequest{WorkspaceID: ws.ID, PlanPath: winFlowPlanPath}, &mission, 200)
	var detail domain.PlanDetail
	h.want(t, "GET", base+"/plan?workspace_id="+ws.ID, nil, &detail, 200)
	if !bytes.Equal(detail.Content, []byte(winFlowPlan)) || detail.Identity.Version != domain.PlanVersion(detail.Content) || detail.Identity.MissionID != mission.ID || detail.Identity.Binding.WorkspaceID != ws.ID || detail.Identity.Binding.CanonicalRoot != root {
		t.Fatal("displayed plan bytes/identity", detail)
	}
	var result domain.PlanApprovalResult
	h.want(t, "POST", base+"/plan/approvals", detail.Identity, &result, 200)
	if result.Effect != domain.PlanApprovalRecorded || result.Receipt == nil || result.Target != detail.Identity || result.Receipt.Identity != detail.Identity || !bytes.Equal(result.Receipt.Content, detail.Content) {
		t.Fatal("approval differs from displayed bytes", result)
	}
	disk, err := os.ReadFile(filepath.Join(root, winFlowPlanPath))
	winFlowCheck(t, err)
	if !bytes.Equal(disk, detail.Content) {
		t.Fatal("approval changed plan")
	}
	return *result.Receipt
}
func winHTTPBase(a domain.PlanApproval) string { return "/v0/missions/" + a.Identity.MissionID }
func (h *winFlowHTTP) state(t *testing.T, a domain.PlanApproval) domain.ExecutionState {
	t.Helper()
	var state domain.ExecutionState
	h.want(t, "GET", winHTTPBase(a)+"/execution?workspace_id="+a.Identity.Binding.WorkspaceID+"&approval_id="+a.ID, nil, &state, 200)
	if _, err := winHTTPReadinessResponse(context.Background(), a, 200, winFlowJSON(t, state)); err != nil {
		t.Fatal("strict state contract", err)
	}
	return state
}

// Only readiness reads may treat the documented, exact plan_io envelope as
// unavailable. A nil state carries no verified facts or mutation authority.
func (h *winFlowHTTP) readiness(t *testing.T, ctx context.Context, a domain.PlanApproval) (*domain.ExecutionState, error) {
	t.Helper()
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("readiness requires a bounded polling context")
	}
	status, raw, err := h.requestContext(t, ctx, http.MethodGet, winHTTPBase(a)+"/execution?workspace_id="+a.Identity.Binding.WorkspaceID+"&approval_id="+a.ID, nil)
	if err != nil {
		return nil, err
	}
	state, err := winHTTPReadinessResponse(ctx, a, status, raw)
	if err == nil && state == nil {
		t.Log("readiness unavailable: no verified current state or authority")
	}
	return state, err
}

func winHTTPReadinessResponse(ctx context.Context, a domain.PlanApproval, status int, raw []byte) (current *domain.ExecutionState, resultErr error) {
	defer func() {
		if cause := context.Cause(ctx); cause != nil {
			current = nil
			resultErr = cause
		}
	}()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	decode := func(out any) error {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(out); err != nil {
			return err
		}
		if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
			return errors.New("readiness: trailing response data")
		}
		return nil
	}
	if status == http.StatusServiceUnavailable {
		var failure struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := decode(&failure); err != nil {
			return nil, err
		}
		if failure.Error != "plan_io" || failure.Message != batuta.ErrArtifactIO.Error() {
			return nil, fmt.Errorf("readiness: unexpected HTTP 503 payload: %s", raw)
		}
		return nil, context.Cause(ctx)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("readiness: unexpected HTTP %d: %s", status, raw)
	}
	var state domain.ExecutionState
	if err := decode(&state); err != nil {
		return nil, err
	}
	if state.Target != a.Identity || state.ApprovalID != a.ID || state.Delivery != domain.ExecutionDelivery(a.ID, a.Identity) {
		return nil, errors.New("readiness: state reference drift")
	}
	if (state.Active && !domain.ValidExecutionRunID(state.RunID)) || (!state.Active && state.RunID != "") {
		return nil, errors.New("readiness: active admission identity drift")
	}
	if state.OpenedDigest == "" {
		if state.Bound || state.Watch != nil || state.Gate != nil {
			return nil, errors.New("readiness: unopened projection")
		}
	} else if state.Watch == nil || state.Gate == nil || !state.Gate.Required {
		return nil, errors.New("readiness: incomplete opened projection")
	}
	if view := state.Watch; view != nil {
		if view.Source != domain.SourceCurrent || view.MissionID != a.Identity.MissionID || view.WorkspaceID != a.Identity.Binding.WorkspaceID || view.Delivery != state.Delivery || state.Gate == nil || state.Gate.Delivery != state.Delivery || view.Progression == nil || *view.Progression != *state.Gate {
			return nil, errors.New("readiness: projection reference drift")
		}
		if q := view.PendingQuestion; q != nil && (q.Target.MissionID != a.Identity.MissionID || q.Target.WorkspaceID != a.Identity.Binding.WorkspaceID || q.Target.Delivery != state.Delivery || q.Target.OpenedDigest != state.OpenedDigest) {
			return nil, errors.New("readiness: question reference drift")
		}
		if state.Gate.Cleared && (state.Gate.ReviewID == "" || state.Gate.EvidenceDigest == "") {
			return nil, errors.New("readiness: cleared gate lacks evidence")
		}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return &state, nil
}

func (h *winFlowHTTP) awaitReadiness(t *testing.T, ctx context.Context, a domain.PlanApproval, what string, check func(domain.ExecutionState) bool) {
	t.Helper()
	winFlowCheck(t, winHTTPAwaitResult(ctx, what, func() (bool, error) {
		state, err := h.readiness(t, ctx, a)
		if err != nil || state == nil {
			return false, err
		}
		return check(*state), nil
	}))
}

func (h *winFlowHTTP) start(t *testing.T, ctx context.Context, a domain.PlanApproval) domain.ExecutionResult {
	t.Helper()
	var result domain.ExecutionResult
	status, raw, err := h.clientCall(t, ctx, func(ctx context.Context, c *client.Client) error {
		var err error
		result, err = c.StartExecution(ctx, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
		return err
	})
	winFlowCheck(t, err)
	var wire domain.ExecutionResult
	winFlowCheck(t, winFlowDecode(raw, &wire))
	if status != 202 || !reflect.DeepEqual(wire, result) {
		t.Fatal("public Start wire", status, wire, result)
	}
	winHTTPAdmission(t, a, result)
	return result
}
func winHTTPAdmission(t *testing.T, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	if r.Error != nil || r.Effect != domain.ExecutionStarted || r.Target != a.Identity || r.ApprovalID != a.ID || r.Delivery != domain.ExecutionDelivery(a.ID, a.Identity) || r.OpenedDigest == "" || !domain.ValidExecutionRunID(r.RunID) {
		t.Fatal("admission identity", r)
	}
}
func (h *winFlowHTTP) resume(t *testing.T, ctx context.Context, a domain.PlanApproval, first domain.ExecutionResult) domain.ExecutionResult {
	t.Helper()
	var result domain.ExecutionResult
	status, raw, err := h.clientCall(t, ctx, func(ctx context.Context, c *client.Client) error {
		var err error
		result, err = c.ResumeExecution(ctx, domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: first.Delivery, OpenedDigest: first.OpenedDigest})
		return err
	})
	winFlowCheck(t, err)
	var wire domain.ExecutionResult
	winFlowCheck(t, winFlowDecode(raw, &wire))
	if status != 202 || !reflect.DeepEqual(wire, result) {
		t.Fatal("public Resume wire", status, wire, result)
	}
	winHTTPAdmission(t, a, result)
	if result.OpenedDigest != first.OpenedDigest || result.RunID == first.RunID {
		t.Fatal("resume authority/opening", result)
	}
	return result
}
func winHTTPStopRef(a domain.PlanApproval, r domain.ExecutionResult) domain.ExecutionStopRequest {
	return domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: r.Delivery, RunID: r.RunID}
}
func (h *winFlowHTTP) command(t *testing.T, ctx context.Context, cmd winFlowCommand) winFlowEvent {
	t.Helper()
	winFlowCheck(t, context.Cause(ctx))
	winFlowCheck(t, h.enc.Encode(cmd))
	var event winFlowEvent
	select {
	case got, ok := <-h.events:
		if !ok {
			t.Fatal("private protocol ended; retaining dependencies")
		}
		event = got
	case <-ctx.Done():
		t.Fatal("private control exceeded inherited budget; retaining dependencies", context.Cause(ctx))
	}
	winFlowCheck(t, context.Cause(ctx))
	if event.Kind != cmd.Op || event.Error != "" {
		t.Fatal("private control mismatch", cmd, event)
	}
	t.Logf("private fixture control=%+v evidence=%s", cmd, winFlowJSON(t, event))
	return event
}
func (h *winFlowHTTP) workspace(t *testing.T, ctx context.Context, ask bool) string {
	t.Helper()
	e := h.command(t, ctx, winFlowCommand{Op: "workspace", Ask: ask})
	if e.Plan != winFlowPlan || e.PlanPath != winFlowPlanPath || e.Root == "" {
		t.Fatal("workspace contract", e)
	}
	root, err := filepath.EvalSymlinks(e.Root)
	winFlowCheck(t, err)
	if root != e.Root {
		t.Fatal("noncanonical fixture root", root, e.Root)
	}
	return root
}
func (h *winFlowHTTP) catalog(t *testing.T, account domain.Account, approvals ...domain.PlanApproval) {
	t.Helper()
	var accounts []domain.Account
	h.want(t, "GET", "/v0/accounts", nil, &accounts, 200)
	if len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].SessionCap != 2 || !accounts[0].Connected {
		t.Fatal("account catalog", accounts)
	}
	var workspaces []domain.Workspace
	h.want(t, "GET", "/v0/workspaces", nil, &workspaces, 200)
	if len(workspaces) != len(approvals) {
		t.Fatal("workspace catalog", workspaces)
	}
	for _, a := range approvals {
		var missions []domain.Mission
		h.want(t, "GET", "/v0/missions?workspace="+a.Identity.Binding.WorkspaceID, nil, &missions, 200)
		if len(missions) != 1 || missions[0].ID != a.Identity.MissionID {
			t.Fatal("independent mission catalog", missions)
		}
		found := false
		for _, w := range workspaces {
			if w.ID == a.Identity.Binding.WorkspaceID && w.Root == a.Identity.Binding.CanonicalRoot {
				found = true
			}
		}
		if !found {
			t.Fatal("canonical workspace absent", a)
		}
	}
}
func (h *winFlowHTTP) receipt(t *testing.T, a domain.PlanApproval) {
	t.Helper()
	var got domain.PlanApproval
	h.want(t, "GET", winHTTPBase(a)+"/plan/approvals/"+a.ID+"?workspace_id="+a.Identity.Binding.WorkspaceID, nil, &got, 200)
	if !reflect.DeepEqual(got, a) {
		t.Fatal("original immutable receipt changed", got, a)
	}
}

// These retained handles witness original births, never a later PID occupant.
type winHTTPTree struct {
	birth   winFlowTree
	handles []windows.Handle
}

func (h *winFlowHTTP) tree(t *testing.T, ctx context.Context, a domain.PlanApproval, phase string) winHTTPTree {
	t.Helper()
	e := h.command(t, ctx, winFlowCommand{Op: "tree", Root: a.Identity.Binding.CanonicalRoot, Phase: phase})
	if e.Tree == nil || e.Tree.Core == nil {
		t.Fatal("actual Core/provider/descendant required", e)
	}
	out := winHTTPTree{birth: *e.Tree}
	for _, b := range []winFlowBirth{e.Tree.Root, e.Tree.Child, *e.Tree.Core} {
		if b.PID == 0 || b.Created == 0 {
			t.Fatal("empty native birth", b)
		}
		handle, err := winFlowWitness(b)
		winFlowCheck(t, err)
		out.handles = append(out.handles, handle)
		t.Cleanup(func() { winFlowCheck(t, windows.CloseHandle(handle)) })
	}
	out.alive(t)
	return out
}
func (tree winHTTPTree) alive(t *testing.T) {
	t.Helper()
	for _, h := range tree.handles {
		status, err := windows.WaitForSingleObject(h, 0)
		winFlowCheck(t, err)
		if status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("original birth unexpectedly retired", tree.birth, status)
		}
	}
}
func (tree winHTTPTree) retired(t *testing.T, ctx context.Context, core bool) {
	t.Helper()
	handles := tree.handles[:2]
	if core {
		handles = tree.handles
	}
	winFlowCheck(t, winHTTPAwaitResult(ctx, "original native birth retirement", func() (bool, error) {
		for _, h := range handles {
			status, err := windows.WaitForSingleObject(h, 0)
			if err != nil {
				return false, err
			}
			if status == uint32(windows.WAIT_TIMEOUT) {
				return false, nil
			}
			if status != windows.WAIT_OBJECT_0 {
				return false, fmt.Errorf("unexpected birth state %d", status)
			}
		}
		return true, nil
	}))
}
func (h *winFlowHTTP) observe(t *testing.T, ctx context.Context, op string, a domain.PlanApproval, phase string) {
	t.Helper()
	h.command(t, ctx, winFlowCommand{Op: op, Root: a.Identity.Binding.CanonicalRoot, Phase: phase})
}
func winHTTPJournal(t *testing.T, ctx context.Context, a domain.PlanApproval, delivery string) []journal.Record {
	t.Helper()
	f, err := os.Open(filepath.Join(a.Identity.Binding.CanonicalRoot, ".batuta", "journal", delivery+".jsonl"))
	winFlowCheck(t, err)
	snapshot, err := journal.ReadSnapshot(ctx, f)
	winFlowCheck(t, errors.Join(err, f.Close(), context.Cause(ctx)))
	if !snapshot.Complete() {
		t.Fatal("incomplete persisted fixture journal")
	}
	return snapshot.Records()
}
func winHTTPOpening(t *testing.T, records []journal.Record, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	count := 0
	for _, rec := range records {
		if rec.Kind == loop.KindOpened {
			count++
			var detail map[string]json.RawMessage
			winFlowCheck(t, json.Unmarshal(rec.Detail, &detail))
			var receipt, version, root string
			winFlowCheck(t, json.Unmarshal(detail["approval_receipt_id"], &receipt))
			winFlowCheck(t, json.Unmarshal(detail["plan_content_digest"], &version))
			winFlowCheck(t, json.Unmarshal(detail["workspace"], &root))
			if rec.Digest != r.OpenedDigest || receipt != a.ID || version != a.Identity.Version || root != a.Identity.Binding.CanonicalRoot {
				t.Fatal("persisted opening differs from original approval", rec)
			}
		}
	}
	if count != 1 {
		t.Fatal("opening count", count)
	}
}
func winHTTPPending(t *testing.T, out domain.ExecutionStopResult, status int) {
	t.Helper()
	if status != 202 || out.Effect != domain.ExecutionStopPending || out.Finalized || out.Durable || out.CoreState != "" || out.State != nil || out.Error != nil || len(out.Causes) != 0 {
		t.Fatal("held Stop falsely finalized", status, out)
	}
}
func (h *winFlowHTTP) stopContext(t *testing.T, ctx context.Context, req domain.ExecutionStopRequest) (domain.ExecutionStopResult, int, error) {
	t.Helper()
	var result domain.ExecutionStopResult
	status, raw, err := h.clientCall(t, ctx, func(ctx context.Context, c *client.Client) error {
		var err error
		result, err = c.StopExecution(ctx, req)
		return err
	})
	var wire domain.ExecutionStopResult
	winFlowCheck(t, winFlowDecode(raw, &wire))
	if wire.ExecutionStopRequest != req || !reflect.DeepEqual(wire, result) {
		t.Fatal("public client lost full Stop wire/causes", wire, result, err)
	}
	if err != nil {
		var typed *client.ExecutionStopError
		if !errors.As(err, &typed) || typed.Status != status || !reflect.DeepEqual(typed.Result, result) || typed.Cause != nil {
			t.Fatal("Stop error was not exact typed API failure", err)
		}
	}
	return result, status, err
}
func (h *winFlowHTTP) rejectStop(t *testing.T, ctx context.Context, req domain.ExecutionStopRequest) {
	t.Helper()
	out, status, err := h.stopContext(t, ctx, req)
	if err == nil || status != 400 || out.Effect != domain.ExecutionStopRejected || out.Finalized || out.Durable || out.State != nil || out.CoreState != "" || len(out.Causes) != 0 || out.Error == nil || out.Error.Code != "plan_reference" || out.Error.Message != domain.ErrInvalidReference.Error() {
		t.Fatal("old/foreign nonce contract", status, out, err)
	}
}
func (h *winFlowHTTP) finishedStop(t *testing.T, ctx context.Context, a domain.PlanApproval, r domain.ExecutionResult) domain.ExecutionStopResult {
	t.Helper()
	req := winHTTPStopRef(a, r)
	var final domain.ExecutionStopResult
	winFlowCheck(t, winHTTPAwaitResult(ctx, "durable exact HTTP Stop", func() (bool, error) {
		out, status, err := h.stopContext(t, ctx, req)
		if err != nil {
			return false, err
		}
		if status == 202 {
			winHTTPPending(t, out, status)
			return false, nil
		}
		if status != 200 || out.Effect != domain.ExecutionStopCompleted || !out.Finalized || !out.Durable || out.CoreState != loop.StateCanceled || out.State == nil || out.State.Active || out.State.RunID != "" || out.State.OpenedDigest != r.OpenedDigest || out.State.Watch == nil || out.State.Watch.Header["state"] != loop.StateCanceled || !strings.Contains(strings.Join(out.Causes, "\n"), context.Canceled.Error()) {
			return false, fmt.Errorf("invalid durable cancellation: %+v", out)
		}
		final = out
		return true, nil
	}))
	return final
}
func (h *winFlowHTTP) ship(t *testing.T, ctx context.Context, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	h.awaitReadiness(t, ctx, a, "public inactive SHIP with required cleared gate", func(s domain.ExecutionState) bool {
		if s.Active || s.Watch == nil || s.Gate == nil || !s.Gate.Cleared {
			return false
		}
		if !s.Bound || s.RunID != "" || s.OpenedDigest != r.OpenedDigest || s.Delivery != r.Delivery || s.Watch.Header["state"] != loop.StateDone || !s.Gate.Required || s.Gate.EvidenceDigest == "" {
			t.Fatal("public SHIP contract", s)
		}
		return true
	})
	// Persisted artifacts supplement the public assertions; they never grant API success.
	observation, err := loop.ObserveSupervision(loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: r.Delivery})
	winFlowCheck(t, err)
	if observation.Review == nil || observation.Review.State != "reported" || observation.Review.Outcome != "SHIP" || observation.Review.ApprovalReceiptID != a.ID {
		t.Fatal("actual Core review evidence", observation)
	}
	raw, err := os.ReadFile(filepath.Join(a.Identity.Binding.CanonicalRoot, "out.txt"))
	winFlowCheck(t, err)
	if string(raw) != "approved output\n" {
		t.Fatal("canonical output integration", string(raw))
	}
	winHTTPOpening(t, winHTTPJournal(t, ctx, a, r.Delivery), a, r)
	h.receipt(t, a)
}
func (h *winFlowHTTP) drain(t *testing.T) {
	t.Helper()
	h.close(t)
	if !h.joined {
		t.Fatal("host/HTTP/SDK/manager/reader/control/foreign drain not joined; dependencies retained", h.cfg.Root)
	}
}

func TestWindowsPublicFlowHTTPTwoMissionsStopRestartSHIP(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := winFlowFixture(t, provider, true)
			h := &winFlowHTTP{winFlowLaunch(t, cfg)}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			rootA, rootB := h.workspace(t, ctx, true), h.workspace(t, ctx, false)
			account := winHTTPAccount(t, h)
			a, b := winHTTPApprove(t, h, rootA, account), winHTTPApprove(t, h, rootB, account)
			if a.ID == b.ID || a.Identity.MissionID == b.Identity.MissionID || a.Identity.Binding.WorkspaceID == b.Identity.Binding.WorkspaceID || strings.EqualFold(a.Identity.Binding.CanonicalRoot, b.Identity.Binding.CanonicalRoot) {
				t.Fatal("missions not independent")
			}
			h.catalog(t, account, a, b)
			h.authControl(t, ctx, a)
			first := h.start(t, ctx, a)
			var question domain.PendingQuestion
			h.awaitReadiness(t, ctx, a, "A actual question and idle", func(s domain.ExecutionState) bool {
				if s.Active || s.Watch == nil || s.Watch.PendingQuestion == nil {
					return false
				}
				question = *s.Watch.PendingQuestion
				return true
			})
			q := question.Target
			if q.MissionID != a.Identity.MissionID || q.WorkspaceID != a.Identity.Binding.WorkspaceID || q.Delivery != first.Delivery || q.Task == "" || q.Execution < 1 || q.QuestionID == "" || q.OpenedDigest != first.OpenedDigest || question.Prompt != "exact native question?" {
				t.Fatal("complete question identity", question)
			}
			before := winHTTPJournal(t, ctx, a, first.Delivery)
			var answer domain.AnswerResult
			status, raw, err := h.clientCall(t, ctx, func(ctx context.Context, c *client.Client) error {
				var err error
				answer, err = c.AnswerQuestion(ctx, q.Answer("exact native\nanswer"))
				return err
			})
			winFlowCheck(t, err)
			var answerWire domain.AnswerResult
			winFlowCheck(t, winFlowDecode(raw, &answerWire))
			if status != 200 || !reflect.DeepEqual(answer, answerWire) || answer.Effect != domain.AnswerRecorded || answer.Target != q || answer.Delivery != first.Delivery {
				t.Fatal("exact multiline answer", status, answer)
			}
			state := h.state(t, a)
			after := winHTTPJournal(t, ctx, a, first.Delivery)
			if state.Active || state.RunID != "" || state.Watch == nil || state.Watch.PendingQuestion != nil || len(after) != len(before)+1 || !reflect.DeepEqual(before, after[:len(before)]) || after[len(after)-1].Kind != loop.KindAnswer {
				t.Fatal("answer auto-admitted or rewrote journal", state)
			}
			var detail struct {
				Execution int    `json:"execution"`
				Answer    string `json:"answer"`
				Question  string `json:"question"`
			}
			winFlowCheck(t, winFlowDecode(after[len(after)-1].Detail, &detail))
			if detail.Answer != "exact native\nanswer" || detail.Execution != q.Execution || detail.Question != question.Prompt {
				t.Fatal("persisted answer bytes", detail)
			}
			if _, err := os.Stat(filepath.Join(rootA, ".git", "public-flow", "first.birth.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("answer launched held provider", err)
			}
			aRun := h.resume(t, ctx, a, first)
			target := h.tree(t, ctx, a, "first")
			bRun := h.start(t, ctx, b)
			sibling := h.tree(t, ctx, b, "first")
			if aRun.RunID == bRun.RunID || aRun.Delivery == bRun.Delivery || target.birth.Root == sibling.birth.Root || *target.birth.Core == *sibling.birth.Core {
				t.Fatal("admissions/native jobs not independent")
			}
			target.alive(t)
			sibling.alive(t)
			for _, entry := range []struct {
				a domain.PlanApproval
				r domain.ExecutionResult
			}{{a, aRun}, {b, bRun}} {
				s := h.state(t, entry.a)
				if !s.Active || s.RunID != entry.r.RunID || s.OpenedDigest != entry.r.OpenedDigest {
					t.Fatal("capacity two not occupied by actual admissions", s)
				}
			}
			foreign := h.command(t, ctx, winFlowCommand{Op: "foreign"})
			if foreign.Tree == nil || foreign.Tree.Root.PID == 0 || foreign.Tree.Root.Created == 0 {
				t.Fatal("foreign witness missing", foreign)
			}
			req := winHTTPStopRef(a, aRun)
			wrong := req
			wrong.RunID = bRun.RunID
			h.rejectStop(t, ctx, wrong)
			target.alive(t)
			sibling.alive(t)
			lock := winFlowCommand{Op: "hold", Root: rootA, Delivery: aRun.Delivery}
			if !h.command(t, ctx, lock).Blocked {
				t.Fatal("real journal reader not excluded")
			}
			// This entire interval is stricter than Core's existing five-second unwind.
			stopCtx, stopCancel := context.WithTimeout(ctx, 5*time.Second)
			defer stopCancel()
			pending, status, err := h.stopContext(t, stopCtx, req)
			winFlowCheck(t, err)
			winHTTPPending(t, pending, status)
			target.retired(t, stopCtx, false)
			h.observe(t, stopCtx, "retired", a, "first")
			h.observe(t, stopCtx, "core-alive", a, "first")
			held, status, err := h.stopContext(t, stopCtx, req)
			winFlowCheck(t, err)
			winHTTPPending(t, held, status)
			if !reflect.DeepEqual(pending, held) {
				t.Fatal("same-target observation changed pending response", pending, held)
			}
			sibling.alive(t)
			h.command(t, stopCtx, winFlowCommand{Op: "foreign-alive"})
			lock.Op = "unlock"
			h.command(t, stopCtx, lock)
			final := h.finishedStop(t, stopCtx, a, aRun)
			target.retired(t, stopCtx, true)
			h.observe(t, stopCtx, "core-retired", a, "first")
			stopCancel()
			sibling.alive(t)
			h.observe(t, ctx, "release", b, "first")
			h.ship(t, ctx, b, bRun)
			sibling.retired(t, ctx, true)
			h.observe(t, ctx, "core-retired", b, "first")
			h.readinessControl(t, ctx, b, bRun)
			aRecords, bRecords := winHTTPJournal(t, ctx, a, aRun.Delivery), winHTTPJournal(t, ctx, b, bRun.Delivery)
			winHTTPOpening(t, aRecords, a, first)
			h.command(t, ctx, winFlowCommand{Op: "foreign-alive"})
			h.drain(t)
			h = &winFlowHTTP{winFlowLaunch(t, cfg)}
			recovered := h.state(t, a)
			if recovered.Active || recovered.RunID != "" || recovered.Delivery != first.Delivery || recovered.OpenedDigest != first.OpenedDigest || recovered.Watch == nil || recovered.Watch.Header["state"] != loop.StateCanceled || recovered.Target != final.State.Target {
				t.Fatal("restart lost authoritative cancellation", recovered)
			}
			if !reflect.DeepEqual(aRecords, winHTTPJournal(t, ctx, a, aRun.Delivery)) || !reflect.DeepEqual(bRecords, winHTTPJournal(t, ctx, b, bRun.Delivery)) {
				t.Fatal("restart replayed or changed actual journals")
			}
			h.receipt(t, a)
			h.receipt(t, b)
			h.ship(t, ctx, b, bRun)
			h.rejectStop(t, ctx, req)
			h.observe(t, ctx, "phase", a, "replacement")
			next := h.resume(t, ctx, a, first)
			if next.RunID == aRun.RunID || next.RunID == bRun.RunID {
				t.Fatal("reused admission nonce")
			}
			replacement := h.tree(t, ctx, a, "replacement")
			if replacement.birth.Root == target.birth.Root || replacement.birth.Child == target.birth.Child || *replacement.birth.Core == *target.birth.Core {
				t.Fatal("replacement reused original birth identity")
			}
			h.rejectStop(t, ctx, req)
			state = h.state(t, a)
			if !state.Active || state.RunID != next.RunID {
				t.Fatal("stale Stop canceled replacement", state)
			}
			replacement.alive(t)
			h.observe(t, ctx, "release", a, "replacement")
			h.ship(t, ctx, a, next)
			replacement.retired(t, ctx, true)
			h.observe(t, ctx, "core-retired", a, "replacement")
			if !reflect.DeepEqual(bRecords, winHTTPJournal(t, ctx, b, bRun.Delivery)) {
				t.Fatal("A replacement changed B journal")
			}
			h.drain(t)
		})
	}
}

func TestWindowsPublicFlowHTTPStopJournalFault(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := winFlowFixture(t, provider, true)
			h := &winFlowHTTP{winFlowLaunch(t, cfg)}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			root := h.workspace(t, ctx, false)
			a := winHTTPApprove(t, h, root, winHTTPAccount(t, h))
			run := h.start(t, ctx, a)
			tree := h.tree(t, ctx, a, "first")
			e := h.command(t, ctx, winFlowCommand{Op: "foreign"})
			if e.Tree == nil || e.Tree.Root.PID == 0 {
				t.Fatal("foreign witness missing")
			}
			lock := winFlowCommand{Op: "hold", Root: root, Delivery: run.Delivery, Phase: "first"}
			if !h.command(t, ctx, lock).Blocked {
				t.Fatal("real journal lock missing")
			}
			stopCtx, stopCancel := context.WithTimeout(ctx, 5*time.Second)
			defer stopCancel()
			req := winHTTPStopRef(a, run)
			pending, status, err := h.stopContext(t, stopCtx, req)
			winFlowCheck(t, err)
			winHTTPPending(t, pending, status)
			tree.retired(t, stopCtx, false)
			h.observe(t, stopCtx, "retired", a, "first")
			h.observe(t, stopCtx, "core-alive", a, "first")
			held, status, err := h.stopContext(t, stopCtx, req)
			winFlowCheck(t, err)
			winHTTPPending(t, held, status)
			if !reflect.DeepEqual(held, pending) {
				t.Fatal("fault pending observation drift")
			}
			h.command(t, stopCtx, winFlowCommand{Op: "foreign-alive"})
			// Qualified control appends through the original descriptor, Syncs,
			// checks both handle and named Windows file IDs, then unlocks immediately.
			lock.Op = "corrupt"
			if !h.command(t, stopCtx, lock).SameFile {
				t.Fatal("fault did not preserve original file ID")
			}
			// Core may return its canceled intent even when its terminal append fails.
			// Neither that string nor process retirement establishes a durable terminal.
			var final domain.ExecutionStopResult
			winFlowCheck(t, winHTTPAwaitResult(stopCtx, "actual 503 uncertain finalization", func() (bool, error) {
				out, status, err := h.stopContext(t, stopCtx, req)
				if status == 202 {
					winFlowCheck(t, err)
					winHTTPPending(t, out, status)
					return false, nil
				}
				causes := strings.Join(out.Causes, "\n")
				if status != 503 || err == nil || out.Effect != domain.ExecutionStopUncertain || !out.Finalized || out.Durable || (out.CoreState != "" && out.CoreState != loop.StateCanceled) || out.State != nil || out.Error == nil || out.Error.Code != "execution_stop_uncertain" || out.Error.Message != "native finalization requires inspection" || !strings.Contains(causes, context.Canceled.Error()) || !strings.Contains(causes, batuta.ErrArtifactInvalid.Error()) {
					return false, fmt.Errorf("fault lost independent native causes or invented durable state: status=%d result=%+v err=%v", status, out, err)
				}
				final = out
				return true, nil
			}))
			tree.retired(t, stopCtx, true)
			h.observe(t, stopCtx, "core-retired", a, "first")
			stopCancel()
			repeated, status, err := h.stopContext(t, ctx, req)
			if status != 503 || err == nil || !reflect.DeepEqual(repeated, final) {
				t.Fatal("uncertain observation lost full causes", status, repeated, err)
			}
			h.command(t, ctx, winFlowCommand{Op: "foreign-alive"})
			h.receipt(t, a)
			h.drain(t)
		})
	}
}

func (h *winFlowHTTP) authControl(t *testing.T, ctx context.Context, a domain.PlanApproval) {
	t.Helper()
	before := h.state(t, a)
	for _, token := range []string{"", "foreign-token"} {
		unauthorized := &winFlowHTTP{&winFlowHost{address: h.address, token: token}}
		for _, method := range []string{"GET", "POST"} {
			path := winHTTPBase(a) + "/execution"
			var body []byte
			if method == "GET" {
				path += "?workspace_id=" + a.Identity.Binding.WorkspaceID + "&approval_id=" + a.ID
			} else {
				body = winFlowJSON(t, domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID})
			}
			status, raw, err := unauthorized.requestContext(t, ctx, method, path, body)
			winFlowCheck(t, err)
			var failure struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			winFlowCheck(t, winFlowDecode(raw, &failure))
			if status != 401 || failure.Error != "unauthorized" || failure.Message != "missing or invalid token" {
				t.Fatal("authentication contract", status, failure)
			}
		}
	}
	body := winFlowJSON(t, map[string]any{"target": a.Identity, "approval_id": a.ID, "unknown": true})
	status, raw, err := h.requestContext(t, ctx, "POST", winHTTPBase(a)+"/execution", body)
	winFlowCheck(t, err)
	var rejected domain.ExecutionResult
	winFlowCheck(t, winFlowDecode(raw, &rejected))
	if status != 400 || rejected.Effect != domain.ExecutionRejected || rejected.RunID != "" || rejected.Delivery != "" || rejected.OpenedDigest != "" || rejected.Error == nil || rejected.Error.Code != "execution_request" || rejected.Error.Message != "invalid execution request" {
		t.Fatal("unknown mutation field accepted", status, rejected)
	}
	after := h.state(t, a)
	if !reflect.DeepEqual(before, after) || after.Active || after.RunID != "" || after.OpenedDigest != "" {
		t.Fatal("unauthorized request admitted work", before, after)
	}
}

// Lock an existing actual Core review guard at byte zero. This is deliberately
// distinct from the journal coordination byte used by the qualified host.
func winHTTPReviewGuard(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	winFlowCheck(t, err)
	lock := &winFlowJournalLock{file: file}
	winFlowCheck(t, lock.lock())
	release := func() { winFlowCheck(t, lock.close()) }
	t.Cleanup(release)
	other, err := os.OpenFile(path, os.O_RDWR, 0)
	winFlowCheck(t, err)
	first, err := winFlowFileID(file)
	winFlowCheck(t, err)
	second, err := winFlowFileID(other)
	winFlowCheck(t, err)
	if first != second {
		t.Fatal("review guard file ID changed")
	}
	probe := &winFlowJournalLock{file: other}
	err = probe.lock()
	closeErr := probe.close()
	winFlowCheck(t, closeErr)
	if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatal("independent review guard did not block", err)
	}
	return release
}
func (h *winFlowHTTP) readinessControl(t *testing.T, ctx context.Context, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	baseline := h.state(t, a)
	records := winHTTPJournal(t, ctx, a, r.Delivery)
	if baseline.Gate == nil || !baseline.Gate.Cleared || baseline.Gate.ReviewID == "" {
		t.Fatal("real completed review required")
	}
	guard := filepath.Join(a.Identity.Binding.CanonicalRoot, ".batuta", "reviews", "supervision", baseline.Gate.ReviewID, "ownership.guard")
	release := winHTTPReviewGuard(t, guard)
	var unavailable domain.ExecutionState
	status, raw, err := h.clientCall(t, ctx, func(ctx context.Context, c *client.Client) error {
		var err error
		unavailable, err = c.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
		return err
	})
	var executionErr *client.ExecutionError
	var planErr *client.PlanError
	if status != 503 || !errors.As(err, &executionErr) || executionErr.Status != 503 || !errors.As(err, &planErr) || planErr.Code != "plan_io" || planErr.Message != batuta.ErrArtifactIO.Error() || !reflect.DeepEqual(unavailable, domain.ExecutionState{}) {
		t.Fatal("strict public client hid guard unavailability", unavailable, err)
	}
	state, err := winHTTPReadinessResponse(ctx, a, status, raw)
	winFlowCheck(t, err)
	if state != nil {
		t.Fatal("guard granted cached state")
	}
	budget, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	observed := false
	err = winHTTPAwaitResult(budget, "persistent actual guard unavailability", func() (bool, error) {
		state, err := h.readiness(t, budget, a)
		if err != nil {
			return false, err
		}
		if state != nil {
			return false, errors.New("held guard granted state")
		}
		observed = true
		stop(context.DeadlineExceeded)
		return false, nil
	})
	if !observed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired unavailable wait succeeded", observed, err)
	}
	release()
	calls := 0
	err = winHTTPAwaitResult(budget, "recovery cannot rescue expired wait", func() (bool, error) { calls++; return true, nil })
	if calls != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("post-deadline inspection", calls, err)
	}
	fresh, err := h.readiness(t, ctx, a)
	winFlowCheck(t, err)
	if fresh == nil || fresh.Active || fresh.RunID != "" || fresh.Target != baseline.Target || fresh.ApprovalID != a.ID || fresh.Delivery != r.Delivery || fresh.OpenedDigest != r.OpenedDigest || !reflect.DeepEqual(fresh.Gate, baseline.Gate) || fresh.Watch == nil || fresh.Watch.Header["state"] != loop.StateDone {
		t.Fatal("fresh recovery changed actual completed evidence", fresh)
	}
	late, cancelLate := context.WithCancelCause(ctx)
	defer cancelLate(nil)
	err = winHTTPAwaitResult(late, "actual 200 at expiry barrier", func() (bool, error) {
		state, err := h.readiness(t, late, a)
		if err != nil || state == nil {
			return false, errors.Join(err, errors.New("missing actual fresh state"))
		}
		cancelLate(context.DeadlineExceeded)
		return true, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("late 200 rescued expired wait", err)
	}
	if !reflect.DeepEqual(records, winHTTPJournal(t, ctx, a, r.Delivery)) {
		t.Fatal("readiness modified persisted journal")
	}
	h.receipt(t, a)
	t.Log("readiness-only guard control: actual Core review guard/503/recovery; public plan_io envelope does not identify arbitrary I/O faults as guard contention")
}

func TestWindowsPublicFlowHTTPReadinessProtocol(t *testing.T) {
	// Synthetic wire negatives qualify only this adapter, never a provider run.
	a := domain.PlanApproval{ID: "receipt"}
	a.Identity.MissionID = "mission"
	valid := domain.ExecutionState{Target: a.Identity, ApprovalID: a.ID, Delivery: domain.ExecutionDelivery(a.ID, a.Identity)}
	wrong := valid
	wrong.ApprovalID = "foreign"
	for _, tc := range []struct {
		name   string
		status int
		raw    string
	}{
		{"malformed-503", 503, `{`},
		{"missing-message", 503, `{"error":"plan_io"}`},
		{"unknown-message", 503, `{"error":"plan_io","message":"unavailable"}`},
		{"other-code", 503, `{"error":"plan_store","message":"artifact could not be read"}`},
		{"nested-error", 503, `{"error":{"code":"plan_io","message":"artifact could not be read"}}`},
		{"extra-state", 503, `{"error":"plan_io","message":"artifact could not be read","active":false}`},
		{"trailing-json", 503, `{"error":"plan_io","message":"artifact could not be read"} {}`},
		{"wrong-status", 500, `{"error":"plan_io","message":"artifact could not be read"}`},
		{"redirect", 302, `{}`},
		{"accepted-not-state", 202, string(winFlowJSON(t, valid))},
		{"foreign-state", 200, string(winFlowJSON(t, wrong))},
		{"empty-state", 200, `{}`},
		{"malformed-state", 200, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			calls := 0
			err := winHTTPAwaitResult(ctx, tc.name, func() (bool, error) {
				calls++
				if calls > 1 {
					cancel(errors.New("unexpected protocol retry"))
				}
				state, err := winHTTPReadinessResponse(ctx, a, tc.status, []byte(tc.raw))
				if state != nil {
					t.Error("protocol fault granted state", state)
				}
				return state != nil, err
			})
			if err == nil || calls != 1 || context.Cause(ctx) != nil {
				t.Fatal("protocol fault not immediate", calls, err)
			}
		})
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.DeadlineExceeded)
	for _, status := range []int{200, 503} {
		state, err := winHTTPReadinessResponse(ctx, a, status, winFlowJSON(t, valid))
		if state != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("expired parser accepted response", state, err)
		}
	}
}

func TestWindowsPublicFlowHTTPRequestCancellation(t *testing.T) {
	// Synthetic streaming server proves only HTTP cancellation and reader joins.
	entered, joined := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(joined)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := &winFlowHTTP{&winFlowHost{address: strings.TrimPrefix(srv.URL, "http://")}}
	done := make(chan error, 1)
	go func() { _, err := h.readiness(t, ctx, domain.PlanApproval{ID: "negative-control"}); done <- err }()
	select {
	case <-entered:
		cancel()
	case <-ctx.Done():
		t.Error("stream did not enter", ctx.Err())
	}
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Error("request did not inherit cancellation", err)
	}
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Error("HTTP handler failed to join")
	}
}
