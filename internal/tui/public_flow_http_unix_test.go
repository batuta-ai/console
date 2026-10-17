//go:build darwin || linux

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
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func (h *publicFlowHost) request(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var data []byte
	if body != nil {
		data = publicFlowJSON(t, body)
	}
	status, raw, err := h.requestContext(t, context.Background(), method, path, data)
	publicFlowCheck(t, err)
	if out != nil {
		publicFlowCheck(t, json.Unmarshal(raw, out))
	}
	return status
}

func (h *publicFlowHost) requestContext(t *testing.T, parent context.Context, method, path string, data []byte) (int, []byte, error) {
	t.Helper()
	if err := context.Cause(parent); err != nil {
		return 0, nil, err
	}
	// Each call has its own connection. Accepted Stop is observed again by a
	// fresh caller after the first connection/context has been closed.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	ctx, cancel := publicFlowRequestContext(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+h.address+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Logf("HTTP %s %s request=%s transport_error=%v", method, path, data, err)
		return 0, nil, err
	}
	raw, readErr := io.ReadAll(res.Body)
	closeErr := res.Body.Close()
	t.Logf("HTTP %s %s request=%s status=%d response=%s read_error=%v", method, path, data, res.StatusCode, raw, readErr)
	return res.StatusCode, raw, errors.Join(readErr, closeErr, context.Cause(ctx))
}

func publicFlowRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Second)
}

func (h *publicFlowHost) want(t *testing.T, method, path string, body, out any, status int) {
	t.Helper()
	if got := h.request(t, method, path, body, out); got != status {
		t.Fatalf("%s %s: status %d want %d", method, path, got, status)
	}
}
func publicFlowAwait(t *testing.T, ctx context.Context, what string, check func() bool) {
	t.Helper()
	publicFlowCheck(t, publicFlowAwaitResult(ctx, what, func() (bool, error) { return check(), nil }))
}

func publicFlowAwaitResult(ctx context.Context, what string, check func() (bool, error)) error {
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

func TestPublicFlowAwaitCancellation(t *testing.T) {
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
			err := publicFlowAwaitResult(ctx, mode, func() (bool, error) {
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
			err = publicFlowAwaitResult(ctx, "subsequent success cannot rescue expired wait", func() (bool, error) {
				calls++
				return true, nil
			})
			if !errors.Is(err, want) || calls != wantCalls {
				t.Errorf("expired wait rescued: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPublicFlowRequestContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if state, err := (&publicFlowHost{}).readiness(t, context.Background(), domain.PlanApproval{}); err == nil || state != nil {
			t.Fatal("unbounded readiness accepted", state, err)
		}
		parent, stop := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
		defer stop()
		ctx, cancel := publicFlowRequestContext(parent)
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
		capped, release := publicFlowRequestContext(context.Background())
		defer release()
		after := time.Now()
		deadline, ok = capped.Deadline()
		if !ok || deadline.Before(before.Add(10*time.Second)) || deadline.After(after.Add(10*time.Second)) {
			t.Fatal("request cap changed", deadline)
		}
	})
}

const publicFlowPlan = "# Plan — Public flow\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n"
const publicFlowPlanPath = ".batuta/plans/alpha.md"

func publicFlowWorkspace(t *testing.T, cfg publicFlowConfig, ask bool) string {
	t.Helper()
	root := t.TempDir()
	publicFlowWrite(t, filepath.Join(root, publicFlowPlanPath), []byte(publicFlowPlan), 0600)
	publicFlowWrite(t, filepath.Join(root, ".batuta/profile.md"), []byte("# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n"), 0600)
	model := map[string]string{"codex": "qa-native", "claude": "sonnet"}[cfg.Provider]
	routing := "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n"
	for _, lane := range []string{"low", "medium", "high"} {
		routing += "| " + lane + " | * | " + cfg.Provider + " | " + model + " |\n"
	}
	routing += "| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | " + cfg.Provider + " | " + model + " |\n"
	publicFlowWrite(t, filepath.Join(root, ".batuta/routing.md"), []byte(routing), 0600)
	publicFlowWrite(t, filepath.Join(root, ".gitignore"), []byte(".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n"), 0600)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "fixture"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "chore: public flow fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %s %v", raw, err)
		}
	}
	control := filepath.Join(root, ".git/public-flow")
	publicFlowWrite(t, filepath.Join(control, "phase"), []byte("first\n"), 0600)
	if ask {
		publicFlowWrite(t, filepath.Join(control, "ask"), []byte("ask\n"), 0600)
	}
	for _, phase := range []string{"first", "replacement"} {
		publicFlowCheck(t, syscall.Mkfifo(filepath.Join(control, phase+".release"), 0600))
	}
	return root
}
func publicFlowAccount(t *testing.T, h *publicFlowHost) domain.Account {
	t.Helper()
	var account domain.Account
	h.want(t, "POST", "/v0/accounts", map[string]string{"label": "owned", "adapter": h.cfg.Provider}, &account, 200)
	if e := h.control(t, publicFlowControl{Op: "authenticate", AccountID: account.ID}); e.Kind != "authenticated" {
		t.Fatal(e)
	}
	h.want(t, "POST", "/v0/accounts/"+account.ID+"/connect", nil, &account, 200)
	if !account.Connected || account.SessionCap != 2 {
		t.Fatal("account not connected/capacity two", account)
	}
	return account
}
func publicFlowApprove(t *testing.T, h *publicFlowHost, root string, account domain.Account) domain.PlanApproval {
	t.Helper()
	var ws domain.Workspace
	h.want(t, "POST", "/v0/workspaces", map[string]string{"name": "owned", "root": root}, &ws, 200)
	model := map[string]string{"codex": "qa-native", "claude": "sonnet"}[h.cfg.Provider]
	var squad domain.Squad
	h.want(t, "POST", "/v0/squads", domain.Squad{Name: "owned", Members: []domain.Member{{Role: "lead", CLI: h.cfg.Provider, Model: model, AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}}, &squad, 200)
	var mission domain.Mission
	h.want(t, "POST", "/v0/missions", map[string]string{"workspace_id": ws.ID, "title": "owned", "squad_id": squad.ID}, &mission, 200)
	base := "/v0/missions/" + mission.ID
	h.want(t, "PUT", base+"/binding", service.MissionBindingRequest{WorkspaceID: ws.ID, PlanPath: publicFlowPlanPath}, &mission, 200)
	var detail domain.PlanDetail
	h.want(t, "GET", base+"/plan?workspace_id="+ws.ID, nil, &detail, 200)
	if !bytes.Equal(detail.Content, []byte(publicFlowPlan)) || detail.Identity.Version != domain.PlanVersion(detail.Content) || detail.Identity.MissionID != mission.ID || detail.Identity.Binding.WorkspaceID != ws.ID {
		t.Fatal("displayed plan bytes/identity", detail)
	}
	var result domain.PlanApprovalResult
	h.want(t, "POST", base+"/plan/approvals", detail.Identity, &result, 200)
	if result.Effect != domain.PlanApprovalRecorded || result.Receipt == nil || result.Target != detail.Identity || result.Receipt.Identity != detail.Identity || !bytes.Equal(result.Receipt.Content, detail.Content) {
		t.Fatal("approval differs from displayed bytes", result)
	}
	disk, err := os.ReadFile(filepath.Join(root, publicFlowPlanPath))
	publicFlowCheck(t, err)
	if !bytes.Equal(disk, detail.Content) {
		t.Fatal("approval changed plan")
	}
	return *result.Receipt
}
func publicFlowBase(a domain.PlanApproval) string { return "/v0/missions/" + a.Identity.MissionID }
func (h *publicFlowHost) state(t *testing.T, a domain.PlanApproval) domain.ExecutionState {
	t.Helper()
	var state domain.ExecutionState
	h.want(t, "GET", publicFlowBase(a)+"/execution?workspace_id="+a.Identity.Binding.WorkspaceID+"&approval_id="+a.ID, nil, &state, 200)
	if state.Target != a.Identity || state.ApprovalID != a.ID {
		t.Fatal("state reference drift", state)
	}
	return state
}

// Only readiness reads may treat the documented, exact plan_io envelope as
// unavailable. A nil state carries no verified facts or mutation authority.
func (h *publicFlowHost) readiness(t *testing.T, ctx context.Context, a domain.PlanApproval) (*domain.ExecutionState, error) {
	t.Helper()
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("readiness requires a bounded polling context")
	}
	status, raw, err := h.requestContext(t, ctx, http.MethodGet, publicFlowBase(a)+"/execution?workspace_id="+a.Identity.Binding.WorkspaceID+"&approval_id="+a.ID, nil)
	if err != nil {
		return nil, err
	}
	state, err := publicFlowReadinessResponse(ctx, a, status, raw)
	if err == nil && state == nil {
		t.Log("readiness unavailable: no verified current state or authority")
	}
	return state, err
}

func publicFlowReadinessResponse(ctx context.Context, a domain.PlanApproval, status int, raw []byte) (*domain.ExecutionState, error) {
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
		if failure.Error != "plan_io" || failure.Message == "" {
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
	if view := state.Watch; view != nil {
		if view.Source != domain.SourceCurrent || view.MissionID != a.Identity.MissionID || view.WorkspaceID != a.Identity.Binding.WorkspaceID || view.Delivery != state.Delivery || state.Gate == nil || state.Gate.Delivery != state.Delivery || view.Progression == nil || *view.Progression != *state.Gate {
			return nil, errors.New("readiness: projection reference drift")
		}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return &state, nil
}

func (h *publicFlowHost) awaitReadiness(t *testing.T, ctx context.Context, a domain.PlanApproval, what string, check func(domain.ExecutionState) bool) {
	t.Helper()
	publicFlowCheck(t, publicFlowAwaitResult(ctx, what, func() (bool, error) {
		state, err := h.readiness(t, ctx, a)
		if err != nil || state == nil {
			return false, err
		}
		return check(*state), nil
	}))
}

func (h *publicFlowHost) start(t *testing.T, a domain.PlanApproval) domain.ExecutionResult {
	t.Helper()
	var result domain.ExecutionResult
	h.want(t, "POST", publicFlowBase(a)+"/execution", domain.ExecutionRequest{Target: a.Identity, ApprovalID: a.ID}, &result, 202)
	publicFlowAdmission(t, a, result)
	return result
}
func publicFlowAdmission(t *testing.T, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	if r.Effect != domain.ExecutionStarted || r.Target != a.Identity || r.ApprovalID != a.ID || r.Delivery != domain.ExecutionDelivery(a.ID, a.Identity) || r.OpenedDigest == "" || !domain.ValidExecutionRunID(r.RunID) {
		t.Fatal("admission identity", r)
	}
}
func (h *publicFlowHost) resume(t *testing.T, a domain.PlanApproval, first domain.ExecutionResult) domain.ExecutionResult {
	t.Helper()
	var result domain.ExecutionResult
	h.want(t, "POST", publicFlowBase(a)+"/execution/resume", domain.ResumeRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: first.Delivery, OpenedDigest: first.OpenedDigest}, &result, 202)
	publicFlowAdmission(t, a, result)
	if result.OpenedDigest != first.OpenedDigest || result.RunID == first.RunID {
		t.Fatal("resume authority/opening", result)
	}
	return result
}
func publicFlowStopRef(a domain.PlanApproval, r domain.ExecutionResult) domain.ExecutionStopRequest {
	return domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: r.Delivery, RunID: r.RunID}
}
func (h *publicFlowHost) stop(t *testing.T, req domain.ExecutionStopRequest) (domain.ExecutionStopResult, int) {
	t.Helper()
	var out domain.ExecutionStopResult
	status := h.request(t, "POST", "/v0/missions/"+req.Target.MissionID+"/execution/stop", req, &out)
	if out.ExecutionStopRequest != req {
		t.Fatal("Stop reference drift", out, req)
	}
	return out, status
}

type publicFlowBirth struct {
	PID      int
	Identity string
}

func publicFlowObserve(t *testing.T, pid int) publicFlowBirth {
	t.Helper()
	p, err := session.InspectProcess(pid)
	publicFlowCheck(t, err)
	if p.Missing || p.Identity == "" {
		t.Fatal("missing original incarnation", pid, p)
	}
	return publicFlowBirth{pid, p.Identity}
}
func (p publicFlowBirth) alive(t *testing.T) bool {
	t.Helper()
	now, err := session.InspectProcess(p.PID)
	publicFlowCheck(t, err)
	return !now.Missing && now.Identity == p.Identity
}
func publicFlowControlDir(a domain.PlanApproval) string {
	return filepath.Join(a.Identity.Binding.CanonicalRoot, ".git/public-flow")
}
func publicFlowTree(t *testing.T, ctx context.Context, a domain.PlanApproval, phase string) []publicFlowBirth {
	t.Helper()
	var fields []string
	publicFlowAwait(t, ctx, "provider root/descendant birth", func() bool {
		raw, err := os.ReadFile(filepath.Join(publicFlowControlDir(a), phase+".birth"))
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		publicFlowCheck(t, err)
		fields = strings.Fields(string(raw))
		if len(fields) != 2 {
			t.Fatal("invalid birth", string(raw))
		}
		return true
	})
	var tree []publicFlowBirth
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		publicFlowCheck(t, err)
		tree = append(tree, publicFlowObserve(t, pid))
	}
	t.Logf("provider phase=%s mission=%s births=%+v", phase, a.Identity.MissionID, tree)
	return tree
}
func publicFlowRetired(t *testing.T, ctx context.Context, tree []publicFlowBirth) {
	t.Helper()
	publicFlowAwait(t, ctx, "exact original process retirement", func() bool {
		for _, p := range tree {
			if p.alive(t) {
				return false
			}
		}
		return true
	})
}
func publicFlowAlive(t *testing.T, tree ...publicFlowBirth) {
	t.Helper()
	for _, p := range tree {
		if !p.alive(t) {
			t.Fatal("unrelated incarnation retired", p)
		}
	}
}
func publicFlowRelease(t *testing.T, a domain.PlanApproval, phase string) {
	t.Helper()
	fd, err := syscall.Open(filepath.Join(publicFlowControlDir(a), phase+".release"), syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	publicFlowCheck(t, err)
	f := os.NewFile(uintptr(fd), "release")
	_, err = f.WriteString(phase + "\n")
	publicFlowCheck(t, errors.Join(err, f.Close()))
}
func publicFlowJournal(t *testing.T, a domain.PlanApproval, delivery string) []journal.Record {
	t.Helper()
	js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
	publicFlowCheck(t, err)
	records, err := js.Read(delivery)
	publicFlowCheck(t, err)
	return records
}
func publicFlowLock(t *testing.T, a domain.PlanApproval, delivery string) (*os.File, func()) {
	t.Helper()
	js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
	publicFlowCheck(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, err := publicFlowAcquireJournal(ctx, js.Path(delivery))
	publicFlowCheck(t, err)
	released := false
	unlock := func() {
		if released {
			return
		}
		released = true
		publicFlowCheck(t, errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()))
	}
	t.Cleanup(unlock)
	return f, unlock
}
func publicFlowAcquireJournal(ctx context.Context, path string) (_ *os.File, err error) {
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
		}
	}()
	err = publicFlowAwaitResult(ctx, "exclusive fixture journal lock", func() (bool, error) {
		lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN) {
			return false, nil
		}
		return lockErr == nil, lockErr
	})
	if err != nil {
		return nil, err
	}
	// A separate descriptor proves the held inode excludes actual journal readers.
	sentinel, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	lockErr := syscall.Flock(int(sentinel.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	closeErr := sentinel.Close()
	if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
		return nil, errors.Join(fmt.Errorf("journal reader sentinel did not block: %v", lockErr), closeErr)
	}
	if err = errors.Join(closeErr, context.Cause(ctx)); err != nil {
		return nil, err
	}
	return f, nil
}

func TestPublicFlowJournalAcquisition(t *testing.T) {
	for _, mode := range []string{"release", "cancel", "deadline", "already-canceled", "already-expired"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "journal")
				contents := []byte("owned journal bytes\n")
				publicFlowCheck(t, os.WriteFile(path, contents, 0600))
				reader, err := os.Open(path)
				publicFlowCheck(t, err)
				defer reader.Close()
				before, err := reader.Stat()
				publicFlowCheck(t, err)
				publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				cause := errors.New("fixture canceled")
				if mode == "deadline" || mode == "already-expired" {
					var stop context.CancelFunc
					duration := time.Second
					if mode == "already-expired" {
						duration = -time.Second
					}
					ctx, stop = context.WithTimeout(ctx, duration)
					defer stop()
					cause = context.DeadlineExceeded
				}
				if mode == "already-canceled" {
					cancel(cause)
				}
				type result struct {
					f   *os.File
					err error
				}
				done := make(chan result, 1)
				go func() { f, err := publicFlowAcquireJournal(ctx, path); done <- result{f, err} }()
				synctest.Wait()
				if mode == "release" || mode == "cancel" {
					select {
					case got := <-done:
						t.Fatalf("returned while shared lock held: %v", got.err)
					default:
					}
				}
				if mode == "cancel" {
					cancel(cause)
				}
				if mode == "release" {
					publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_UN))
				}
				got := <-done
				if mode == "release" {
					publicFlowCheck(t, got.err)
					if got.f == nil {
						t.Fatal("missing exclusive owner")
					}
					after, err := got.f.Stat()
					publicFlowCheck(t, err)
					if !os.SameFile(before, after) {
						t.Fatal("journal inode changed")
					}
					if err := syscall.Flock(int(reader.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
						t.Fatalf("reader not excluded: %v", err)
					}
					publicFlowCheck(t, got.f.Close())
				} else if got.f != nil || !errors.Is(got.err, cause) {
					t.Fatalf("ownership=%v error=%v want cause=%v", got.f, got.err, cause)
				}
				publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_UN))
				publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
				raw, err := os.ReadFile(path)
				publicFlowCheck(t, err)
				if !bytes.Equal(raw, contents) {
					t.Fatal("journal bytes changed")
				}
			})
		})
	}
	t.Run("IO-error", func(t *testing.T) {
		_, err := publicFlowAcquireJournal(context.Background(), filepath.Join(t.TempDir(), "missing"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("open error lost: %v", err)
		}
	})
}

func TestPublicFlowLockLifetime(t *testing.T) {
	var a domain.PlanApproval
	a.Identity.Binding.CanonicalRoot = t.TempDir()
	js, err := journal.Open(a.Identity.Binding.CanonicalRoot)
	publicFlowCheck(t, err)
	path := js.Path("fixture")
	publicFlowCheck(t, os.MkdirAll(filepath.Dir(path), 0700))
	contents := []byte("owned fixture journal\n")
	publicFlowCheck(t, os.WriteFile(path, contents, 0600))
	reader, err := os.Open(path)
	publicFlowCheck(t, err)
	defer reader.Close()
	before, err := reader.Stat()
	publicFlowCheck(t, err)
	var held *os.File
	t.Run("explicit-unlock", func(t *testing.T) {
		f, unlock := publicFlowLock(t, a, "fixture")
		after, err := f.Stat()
		publicFlowCheck(t, err)
		if !os.SameFile(before, after) {
			t.Fatal("journal inode changed")
		}
		unlock()
		unlock()
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("descriptor not closed: %v", err)
		}
		publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
		publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_UN))
	})
	t.Run("cleanup", func(t *testing.T) {
		held, _ = publicFlowLock(t, a, "fixture")
		if err := syscall.Flock(int(reader.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("reader not excluded: %v", err)
		}
	})
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("cleanup left descriptor open: %v", err)
	}
	publicFlowCheck(t, syscall.Flock(int(reader.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	raw, err := os.ReadFile(path)
	publicFlowCheck(t, err)
	if !bytes.Equal(raw, contents) {
		t.Fatal("journal bytes changed")
	}
}

func publicFlowCoreBlocked(t *testing.T, ctx context.Context, h *publicFlowHost) {
	t.Helper()
	publicFlowAwait(t, ctx, "actual Core unwind at journal lock", func() bool {
		event := h.control(t, publicFlowControl{Op: "stacks"})
		for _, stack := range strings.Split(event.Stacks, "\n\ngoroutine ") {
			if strings.Contains(stack, "journal.lockJournalContext") && strings.Contains(stack, "/core/loop.") {
				t.Logf("Core lifetime witness: %s", stack)
				return true
			}
		}
		return false
	})
}
func publicFlowPending(t *testing.T, out domain.ExecutionStopResult, status int) {
	t.Helper()
	if status != 202 || out.Effect != domain.ExecutionStopPending || out.Finalized || out.Durable {
		t.Fatal("held Stop falsely finalized", status, out)
	}
}
func publicFlowFinishedStop(t *testing.T, ctx context.Context, h *publicFlowHost, req domain.ExecutionStopRequest) {
	t.Helper()
	publicFlowAwait(t, ctx, "durable HTTP Stop", func() bool {
		out, status := h.stop(t, req)
		if status == 202 {
			publicFlowPending(t, out, status)
			return false
		}
		if status != 200 || out.Effect != domain.ExecutionStopCompleted || !out.Finalized || !out.Durable || out.CoreState != loop.StateCanceled || out.State == nil || out.State.Active || out.State.RunID != "" || out.State.Watch == nil || out.State.Watch.Header["state"] != loop.StateCanceled {
			t.Fatal("Stop completion", status, out)
		}
		return true
	})
}
func publicFlowShip(t *testing.T, ctx context.Context, h *publicFlowHost, a domain.PlanApproval, r domain.ExecutionResult) {
	t.Helper()
	// Observe Core's real cleared gate before HTTP projection; projection can
	// legitimately reject while the review writer owns its evidence files.
	opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: r.Delivery}
	publicFlowAwait(t, ctx, "qualified SHIP review", func() bool {
		observation, err := loop.ObserveSupervision(opts)
		if err != nil || observation.Review == nil || observation.Review.State != "reported" {
			return false
		}
		if observation.Review.Outcome != "SHIP" || observation.Review.ApprovalReceiptID != a.ID {
			t.Fatal("review identity/outcome", observation)
		}
		gate, err := loop.CheckSupervisionGate(ctx, opts)
		return err == nil && gate.Required && gate.Cleared && gate.EvidenceDigest != ""
	})
	h.awaitReadiness(t, ctx, a, "inactive implementation and public gate", func(s domain.ExecutionState) bool {
		if s.Active {
			return false
		}
		if s.RunID != "" || s.Delivery != r.Delivery || s.OpenedDigest != r.OpenedDigest || s.Watch == nil || s.Watch.Header["state"] != loop.StateDone || s.Gate == nil || !s.Gate.Required || !s.Gate.Cleared || s.Gate.EvidenceDigest == "" {
			t.Fatal("public SHIP", s)
		}
		return true
	})
	raw, err := os.ReadFile(filepath.Join(a.Identity.Binding.CanonicalRoot, "out.txt"))
	publicFlowCheck(t, err)
	if string(raw) != "approved output\n" {
		t.Fatal("canonical integration", string(raw))
	}
	records := publicFlowJournal(t, a, r.Delivery)
	openings := 0
	for _, rec := range records {
		if rec.Kind == loop.KindOpened {
			openings++
			if rec.Digest != r.OpenedDigest {
				t.Fatal("opening changed")
			}
		}
	}
	if openings != 1 {
		t.Fatal("opening count", openings)
	}
}

func TestPublicFlowHTTPTwoMissionsStopRestartSHIP(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := publicFlowBuild(t, provider)
			rootA, rootB := publicFlowWorkspace(t, cfg, true), publicFlowWorkspace(t, cfg, false)
			h := publicFlowLaunch(t, cfg)
			account := publicFlowAccount(t, h)
			a, b := publicFlowApprove(t, h, rootA, account), publicFlowApprove(t, h, rootB, account)
			if a.ID == b.ID || a.Identity.MissionID == b.Identity.MissionID || a.Identity.Binding.WorkspaceID == b.Identity.Binding.WorkspaceID || a.Identity.Binding.CanonicalRoot == b.Identity.Binding.CanonicalRoot {
				t.Fatal("missions not independent")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			first := h.start(t, a)
			var question domain.PendingQuestion
			h.awaitReadiness(t, ctx, a, "A question and idle", func(s domain.ExecutionState) bool {
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
			beforeAnswer := publicFlowJournal(t, a, first.Delivery)
			var answered domain.AnswerResult
			h.want(t, "POST", "/v0/watch/answer", q.Answer("exact native\nanswer"), &answered, 200)
			if answered.Effect != domain.AnswerRecorded || answered.Target != q || answered.Delivery != first.Delivery {
				t.Fatal("answer identity", answered)
			}
			state := h.state(t, a)
			afterAnswer := publicFlowJournal(t, a, first.Delivery)
			if state.Active || state.RunID != "" || len(afterAnswer) != len(beforeAnswer)+1 || afterAnswer[len(afterAnswer)-1].Kind != loop.KindAnswer {
				t.Fatal("answer auto-launched or changed journal", state)
			}
			if _, err := os.Stat(filepath.Join(publicFlowControlDir(a), "first.birth")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("answer launched provider", err)
			}
			aRun := h.resume(t, a, first)
			target := publicFlowTree(t, ctx, a, "first")
			bRun := h.start(t, b)
			sibling := publicFlowTree(t, ctx, b, "first")
			if aRun.RunID == bRun.RunID || aRun.Delivery == bRun.Delivery {
				t.Fatal("admissions not independent")
			}
			foreign := exec.Command("/bin/sleep", "300")
			publicFlowCheck(t, foreign.Start())
			t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
			foreignBirth := publicFlowObserve(t, foreign.Process.Pid)
			req := publicFlowStopRef(a, aRun)
			wrong := req
			wrong.RunID = bRun.RunID
			rejected, status := h.stop(t, wrong)
			if status != 400 || rejected.Effect != domain.ExecutionStopRejected {
				t.Fatal("mismatched authority accepted", status, rejected)
			}
			publicFlowAlive(t, append(target, sibling...)...)
			_, unlock := publicFlowLock(t, a, aRun.Delivery)
			pending, status := h.stop(t, req)
			publicFlowPending(t, pending, status)
			publicFlowRetired(t, ctx, target)
			publicFlowCoreBlocked(t, ctx, h)
			held, status := h.stop(t, req)
			publicFlowPending(t, held, status)
			if !reflect.DeepEqual(pending, held) {
				t.Fatal("fresh caller changed pending reference/body", pending, held)
			}
			publicFlowAlive(t, append(sibling, foreignBirth)...)
			unlock()
			publicFlowFinishedStop(t, ctx, h, req)
			publicFlowRelease(t, b, "first")
			publicFlowShip(t, ctx, h, b, bRun)
			publicFlowRetired(t, ctx, sibling)
			aRecords, bRecords := publicFlowJournal(t, a, aRun.Delivery), publicFlowJournal(t, b, bRun.Delivery)
			oldHost := publicFlowObserve(t, h.cmd.Process.Pid)
			h.close(t)
			if oldHost.alive(t) {
				t.Fatal("old host was not joined")
			}
			h = publicFlowLaunch(t, cfg)
			recovered := h.state(t, a)
			if recovered.Active || recovered.RunID != "" || recovered.Delivery != first.Delivery || recovered.OpenedDigest != first.OpenedDigest || recovered.Watch == nil || recovered.Watch.Header["state"] != loop.StateCanceled {
				t.Fatal("restart lost canceled authority", recovered)
			}
			if !reflect.DeepEqual(aRecords, publicFlowJournal(t, a, aRun.Delivery)) || !reflect.DeepEqual(bRecords, publicFlowJournal(t, b, bRun.Delivery)) {
				t.Fatal("restart mutated/replayed journals")
			}
			for _, approval := range []domain.PlanApproval{a, b} {
				var got domain.PlanApproval
				h.want(t, "GET", publicFlowBase(approval)+"/plan/approvals/"+approval.ID+"?workspace_id="+approval.Identity.Binding.WorkspaceID, nil, &got, 200)
				if !reflect.DeepEqual(got, approval) {
					t.Fatal("restart changed original approval", got)
				}
			}
			publicFlowShip(t, ctx, h, b, bRun)
			rejected, status = h.stop(t, req)
			if status != 400 || rejected.Effect != domain.ExecutionStopRejected {
				t.Fatal("old host authority survived recreation", status, rejected)
			}
			publicFlowWrite(t, filepath.Join(publicFlowControlDir(a), "phase"), []byte("replacement\n"), 0600)
			next := h.resume(t, a, first)
			if next.RunID == aRun.RunID || next.RunID == bRun.RunID {
				t.Fatal("reused admission")
			}
			replacement := publicFlowTree(t, ctx, a, "replacement")
			rejected, status = h.stop(t, req)
			if status != 400 || rejected.Effect != domain.ExecutionStopRejected {
				t.Fatal("old Stop accepted for new A", status, rejected)
			}
			s := h.state(t, a)
			if !s.Active || s.RunID != next.RunID {
				t.Fatal("old Stop canceled new admission", s)
			}
			publicFlowAlive(t, append(replacement, foreignBirth)...)
			publicFlowRelease(t, a, "replacement")
			publicFlowShip(t, ctx, h, a, next)
			publicFlowRetired(t, ctx, replacement)
			h.close(t)
		})
	}
}

func TestPublicFlowHTTPStopJournalFault(t *testing.T) {
	cfg := publicFlowBuild(t, "codex")
	root := publicFlowWorkspace(t, cfg, false)
	h := publicFlowLaunch(t, cfg)
	a := publicFlowApprove(t, h, root, publicFlowAccount(t, h))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started := h.start(t, a)
	tree := publicFlowTree(t, ctx, a, "first")
	f, unlock := publicFlowLock(t, a, started.Delivery)
	req := publicFlowStopRef(a, started)
	out, status := h.stop(t, req)
	publicFlowPending(t, out, status)
	publicFlowRetired(t, ctx, tree)
	publicFlowCoreBlocked(t, ctx, h)
	before, err := f.Stat()
	publicFlowCheck(t, err)
	// Corrupt the held descriptor itself after target retirement, preserving inode.
	_, err = f.WriteString("not-a-journal-record\n")
	publicFlowCheck(t, err)
	publicFlowCheck(t, f.Sync())
	after, err := os.Stat(f.Name())
	publicFlowCheck(t, err)
	if !os.SameFile(before, after) {
		t.Fatal("fault replaced journal inode")
	}
	unlock()
	publicFlowAwait(t, ctx, "HTTP 503 uncertain with independent causes", func() bool {
		result, status := h.stop(t, req)
		if status == 202 {
			publicFlowPending(t, result, status)
			return false
		}
		causes := strings.Join(result.Causes, "\n")
		if status != 503 || result.Effect != domain.ExecutionStopUncertain || !result.Finalized || result.Durable || result.CoreState != "" || result.Error == nil || !strings.Contains(causes, batuta.ErrArtifactInvalid.Error()) || !strings.Contains(causes, context.Canceled.Error()) {
			t.Fatal("journal fault falsely completed/lost causes", status, result)
		}
		return true
	})
	h.close(t)
}

func TestPublicFlowProviderProtocol(t *testing.T) {
	// No listener: qualify the actual provider script's control/capability branches.
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := publicFlowConfig{Provider: provider}
			root := publicFlowWorkspace(t, cfg, true)
			script := filepath.Join(t.TempDir(), provider)
			version := map[string]string{"codex": "codex-cli 0.160.0", "claude": "2.1.289 (Claude Code)"}[provider]
			publicFlowWrite(t, script, []byte(publicFlowProviderScript(version)), 0700)
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.Command(script, args...)
				cmd.Dir = root
				cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				raw, err := cmd.Output()
				if err != nil {
					t.Fatalf("provider %v: %s stderr=%s %v", args, raw, stderr.String(), err)
				}
				return string(raw)
			}
			if run("--version") != version+"\n" || run("debug", "models") != "{\"models\":[{\"slug\":\"qa-native\"}]}\n" || run("doctor") != "{}\n" || run("--verify") != "<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE\n" {
				t.Fatal("protocol branch changed")
			}
			prompt := "Run the shell command `git rev-parse HEAD` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command's output, and stop."
			git := exec.Command("git", "rev-parse", "HEAD")
			git.Dir = root
			head, err := git.Output()
			publicFlowCheck(t, err)
			if got := run("--model", "qa-native", prompt); got != "BATUTA-CAPABLE "+string(head) {
				t.Fatal("actual capability", got)
			}
			if got := run("--model", "qa-native", "ordinary brief"); got != "BATUTA-QUESTION: exact native question?\n" {
				t.Fatal("question stage", got)
			}
			if _, err := os.Stat(filepath.Join(root, ".git/public-flow/first.birth")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("question launched held provider")
			}
			t.Log(fmt.Sprintf("%s exact capability, protocol, question verified", provider))
		})
	}
}

func TestPublicFlowProgressProtocol(t *testing.T) {
	// Synthetic negative wire controls; these do not qualify a provider or Core.
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
		{"other-code", 503, `{"error":"plan_store","message":"unavailable"}`},
		{"nested-error", 503, `{"error":{"code":"plan_io","message":"unavailable"}}`},
		{"extra-state", 503, `{"error":"plan_io","message":"unavailable","active":false}`},
		{"trailing-json", 503, `{"error":"plan_io","message":"unavailable"} {}`},
		{"redirect", 302, `{"error":"plan_io","message":"unavailable"}`},
		{"wrong-status", 500, `{"error":"plan_io","message":"unavailable"}`},
		{"accepted-not-current", 202, string(publicFlowJSON(t, valid))},
		{"malformed-200", 200, `{`},
		{"foreign-200", 200, string(publicFlowJSON(t, wrong))},
		{"empty-200", 200, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			calls := 0
			err := publicFlowAwaitResult(ctx, tc.name, func() (bool, error) {
				calls++
				if calls > 1 {
					cancel(errors.New("unexpected retry"))
				}
				state, err := publicFlowReadinessResponse(ctx, a, tc.status, []byte(tc.raw))
				if state != nil {
					t.Error("invalid response granted current state", state)
				}
				return state != nil, err
			})
			if err == nil || calls != 1 || context.Cause(ctx) != nil {
				t.Fatal("protocol error did not fail immediately", calls, err)
			}
		})
	}
}

// This transport calls the actual API handler, with its actual Service/Store/Core.
// It permits local contract proof without claiming a real HTTP listener journey.
type publicFlowAPITransport struct {
	t       *testing.T
	handler http.Handler
}

func (p publicFlowAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, req)
	p.t.Logf("in-process API %s %s status=%d response=%s", req.Method, req.URL.RequestURI(), w.Code, w.Body.Bytes())
	return w.Result(), nil
}

type publicFlowLoggedTransport struct {
	t         *testing.T
	transport http.RoundTripper
}

func (p publicFlowLoggedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := p.transport.RoundTrip(req)
	if err != nil {
		p.t.Logf("HTTP %s %s transport_error=%v", req.Method, req.URL.RequestURI(), err)
		return nil, err
	}
	raw, readErr := io.ReadAll(res.Body)
	closeErr := res.Body.Close()
	p.t.Logf("HTTP %s %s status=%d response=%s read_error=%v", req.Method, req.URL.RequestURI(), res.StatusCode, raw, readErr)
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	return res, nil
}

func publicFlowProgressFixture(t *testing.T) (*service.Service, http.Handler, domain.PlanApproval, string) {
	t.Helper()
	t.Log("CONTRACT CONTROL: seeded opening/terminal; no provider execution, review receipt, or SHIP evidence")
	root := publicFlowWorkspace(t, publicFlowConfig{Provider: "codex"}, false)
	st, err := store.Open(filepath.Join(t.TempDir(), "contract.db"))
	publicFlowCheck(t, err)
	svc := service.New(st, nil, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := svc.ShutdownNative(ctx); err != nil {
			t.Error("contract drain failed; retaining store", err)
			return
		}
		publicFlowCheck(t, st.Close())
	})
	publicFlowCheck(t, st.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}))
	publicFlowCheck(t, st.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}))
	_, err = svc.BindMission("m", service.MissionBindingRequest{WorkspaceID: "w", PlanPath: publicFlowPlanPath})
	publicFlowCheck(t, err)
	handler := (&api.Server{Svc: svc, Token: "contract"}).Handler()
	c := client.New("http://contract.invalid", "contract")
	c.HTTP.Transport = publicFlowAPITransport{t, handler}
	detail, err := c.PlanDetail(context.Background(), "m", "w")
	publicFlowCheck(t, err)
	approved, err := c.ApprovePlan(context.Background(), detail.Identity)
	publicFlowCheck(t, err)
	if approved.Receipt == nil || approved.Receipt.Identity != detail.Identity || !bytes.Equal(approved.Receipt.Content, detail.Content) {
		t.Fatal("contract approval mismatch", approved)
	}
	a := *approved.Receipt
	binding := a.Identity.Binding
	binding.DeliveryID = domain.ExecutionDelivery(a.ID, a.Identity)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	head, err := cmd.Output()
	publicFlowCheck(t, err)
	js, err := journal.Open(root)
	publicFlowCheck(t, err)
	opened, err := js.Append(binding.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: publicFlowJSON(t, map[string]any{
		"slug": binding.PlanSlug, "plan_path": binding.PlanPath, "plan_digest": binding.PlanDigest,
		"workspace": binding.CanonicalRoot, "approval_receipt_id": a.ID, "plan_content_digest": a.Identity.Version,
		"head": strings.TrimSpace(string(head)), "supervision": true,
	}), Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	publicFlowCheck(t, err)
	binding.OpenedDigest = opened.Digest
	_, err = js.Append(binding.DeliveryID, journal.Record{Kind: loop.KindTerminal, Detail: publicFlowJSON(t, map[string]string{"state": loop.StateDone, "final_commit": strings.TrimSpace(string(head))}), Graph: opened.Graph})
	publicFlowCheck(t, err)
	publicFlowCheck(t, st.BindMission("m", binding))
	gate, err := loop.CheckSupervisionGate(context.Background(), loop.SupervisionOptions{Workspace: root, Delivery: binding.DeliveryID})
	publicFlowCheck(t, err)
	if !gate.Required || gate.Cleared || gate.ReviewID == "" || gate.EvidenceDigest != "" {
		t.Fatal("contract must retain uncleared supervision", gate)
	}
	// Core has created this exact published review guard; it is not the journal lock.
	guard := filepath.Join(root, ".batuta", "reviews", "supervision", gate.ReviewID, "ownership.guard")
	return svc, handler, a, guard
}

func publicFlowReviewGuard(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	publicFlowCheck(t, err)
	publicFlowCheck(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	released := false
	release := func() {
		if !released {
			released = true
			publicFlowCheck(t, errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()))
		}
	}
	t.Cleanup(release)
	witness, err := os.OpenFile(path, os.O_RDWR, 0)
	publicFlowCheck(t, err)
	defer witness.Close()
	first, err := f.Stat()
	publicFlowCheck(t, err)
	second, err := witness.Stat()
	publicFlowCheck(t, err)
	if !os.SameFile(first, second) {
		t.Fatal("review guard witness opened a different inode")
	}
	err = syscall.Flock(int(witness.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatal("independent review guard descriptor did not block", err)
	}
	t.Log("actual review ownership.guard held; independent fd/inode witness returned EWOULDBLOCK")
	return release
}

func TestPublicFlowProgressAPIControl(t *testing.T) {
	publicFlowProgressControl(t, false)
}

func TestPublicFlowProgressHTTPControl(t *testing.T) {
	publicFlowProgressControl(t, true)
}

func publicFlowContractStateMatches(got, want *domain.ExecutionState) bool {
	return got != nil && want != nil && got.Target == want.Target && got.ApprovalID == want.ApprovalID &&
		got.Delivery == want.Delivery && got.OpenedDigest == want.OpenedDigest && got.Bound == want.Bound &&
		!got.Active && got.RunID == "" && reflect.DeepEqual(got.Gate, want.Gate) &&
		got.Watch != nil && got.Watch.Source == domain.SourceCurrent && got.Watch.MissionID == got.Target.MissionID &&
		got.Watch.WorkspaceID == got.Target.Binding.WorkspaceID && got.Watch.Delivery == got.Delivery &&
		got.Watch.Header["state"] == loop.StateReviewBlocked && reflect.DeepEqual(got.Watch.Progression, got.Gate)
}

func publicFlowProgressControl(t *testing.T, realHTTP bool) {
	svc, handler, a, guard := publicFlowProgressFixture(t)
	c := client.New("http://contract.invalid", "contract")
	c.HTTP.Transport = publicFlowAPITransport{t, handler}
	read := func(ctx context.Context) (*domain.ExecutionState, error) {
		req := httptest.NewRequest(http.MethodGet, publicFlowBase(a)+"/execution?workspace_id=w&approval_id="+a.ID, nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer contract")
		res, err := (publicFlowAPITransport{t, handler}).RoundTrip(req)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(res.Body)
		err = errors.Join(err, res.Body.Close())
		if err != nil {
			return nil, err
		}
		return publicFlowReadinessResponse(ctx, a, res.StatusCode, raw)
	}
	if realHTTP {
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		h := &publicFlowHost{address: strings.TrimPrefix(srv.URL, "http://"), token: "contract"}
		read = func(ctx context.Context) (*domain.ExecutionState, error) { return h.readiness(t, ctx, a) }
		c = client.New(srv.URL, "contract")
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
		c.HTTP.Transport = publicFlowLoggedTransport{t, transport}
		t.Cleanup(transport.CloseIdleConnections)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	baseline, err := read(ctx)
	publicFlowCheck(t, err)
	if baseline == nil || baseline.Active || baseline.RunID != "" || !baseline.Bound || baseline.OpenedDigest == "" || baseline.Gate == nil || !baseline.Gate.Required || baseline.Gate.Cleared || baseline.Gate.EvidenceDigest != "" || baseline.Watch == nil || baseline.Watch.Source != domain.SourceCurrent || baseline.Watch.Header["state"] != loop.StateReviewBlocked || baseline.Gate.Delivery != baseline.Delivery || baseline.Watch.Progression == nil || *baseline.Watch.Progression != *baseline.Gate {
		t.Fatal("contract baseline is not an exact, uncleared current projection", baseline)
	}
	records := publicFlowJournal(t, a, baseline.Delivery)
	mission, err := svc.Store.GetMission(a.Identity.MissionID)
	publicFlowCheck(t, err)
	release := publicFlowReviewGuard(t, guard)
	// The ordinary public client stays strict and exposes a typed plan_io error.
	state, err := c.ExecutionState(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
	var executionErr *client.ExecutionError
	var planErr *client.PlanError
	if !errors.As(err, &executionErr) || executionErr.Status != 503 || !errors.As(err, &planErr) || planErr.Code != "plan_io" || !reflect.DeepEqual(state, domain.ExecutionState{}) {
		t.Fatal("held real guard did not produce typed unavailable state", state, err)
	}
	t.Log("strict public client: actual typed HTTP 503 plan_io, zero state; no busy inference")
	unavailable := false
	err = publicFlowAwaitResult(ctx, "held guard then fresh recovery", func() (bool, error) {
		current, err := read(ctx)
		if err != nil {
			return false, err
		}
		if !unavailable {
			if current != nil {
				return false, errors.New("held guard granted cached/current state")
			}
			unavailable = true
			release()
			return false, nil
		}
		if current == nil || !publicFlowContractStateMatches(current, baseline) {
			return false, errors.New("fresh recovery changed exact target/receipt/uncleared gate")
		}
		return true, nil
	})
	publicFlowCheck(t, err)
	if !unavailable {
		t.Fatal("guard control never observed unavailable response")
	}

	for _, fault := range []string{"held-guard", "nonlock-corrupt-review"} {
		t.Run(fault, func(t *testing.T) {
			recoverFault := func() {}
			if fault == "held-guard" {
				recoverFault = publicFlowReviewGuard(t, guard)
			} else {
				// Invalid owned review JSON is an actual non-lock Core I/O failure.
				path := filepath.Join(filepath.Dir(guard), "job.json")
				publicFlowWrite(t, path, []byte("{invalid-review-json\n"), 0600)
				recoverFault = func() { publicFlowCheck(t, os.Remove(path)) }
				defer func() { recoverFault() }()
			}
			// A controlled deadline cause expires the budget only after the actual
			// response. No wall-clock race or arbitrary tolerated-response limit.
			budget, stop := context.WithCancelCause(ctx)
			defer stop(nil)
			observed := false
			err := publicFlowAwaitResult(budget, "persistent unavailability", func() (bool, error) {
				current, err := read(budget)
				if err != nil || current != nil {
					return current != nil, errors.Join(err, errors.New("persistent fault must yield exact 503 and no verified state"))
				}
				observed = true
				stop(context.DeadlineExceeded)
				return false, nil
			})
			if !observed || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("persistent unavailability passed or failed for wrong reason", observed, err)
			}
			// Recovery happens strictly after the wait has failed, never rescuing it.
			recoverFault()
			if fault == "nonlock-corrupt-review" {
				recoverFault = func() {}
			}
			calls := 0
			err = publicFlowAwaitResult(budget, "recovered after expiry", func() (bool, error) {
				calls++
				current, err := read(ctx)
				return current != nil, err
			})
			if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
				t.Fatal("later recovery rescued expired wait", calls, err)
			}
			fresh, err := read(ctx)
			publicFlowCheck(t, err)
			if !publicFlowContractStateMatches(fresh, baseline) {
				t.Fatal("independent fresh observation changed required gate", fresh)
			}
		})
	}

	budget, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	err = publicFlowAwaitResult(budget, "200 obtained while check expires", func() (bool, error) {
		fresh, err := read(budget)
		if err != nil || !publicFlowContractStateMatches(fresh, baseline) {
			return false, errors.Join(err, errors.New("missing fresh 200 before expiry barrier"))
		}
		stop(context.DeadlineExceeded)
		return true, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("late actual 200 accepted", err)
	}
	after, err := svc.Store.GetMission(a.Identity.MissionID)
	publicFlowCheck(t, err)
	saved, err := svc.PlanApproval(ctx, a.Identity.MissionID, a.Identity.Binding.WorkspaceID, a.ID)
	publicFlowCheck(t, err)
	if !reflect.DeepEqual(after, mission) || !reflect.DeepEqual(saved, a) || !reflect.DeepEqual(records, publicFlowJournal(t, a, baseline.Delivery)) {
		t.Fatal("readiness changed binding, receipt, or journal")
	}
	if _, err := os.Stat(filepath.Join(publicFlowControlDir(a), "first.birth")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("readiness launched a provider", err)
	}
}

func TestPublicFlowProgressHTTPCancellation(t *testing.T) {
	// Synthetic streaming negative control, solely for actual request cancellation.
	entered, joined := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(joined)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := &publicFlowHost{address: strings.TrimPrefix(srv.URL, "http://")}
	done := make(chan error, 1)
	go func() {
		_, err := h.readiness(t, ctx, domain.PlanApproval{ID: "negative-control"})
		done <- err
	}()
	started := false
	select {
	case <-entered:
		started = true
		cancel()
	case <-ctx.Done():
		t.Error("stream handler never entered", ctx.Err())
	}
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Error("readiness request did not inherit cancellation", err)
	}
	if started {
		<-joined
	}
}
