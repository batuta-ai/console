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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func (h *publicFlowHost) request(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var data []byte
	if body != nil {
		data = publicFlowJSON(t, body)
	}
	// Each call has its own connection. Accepted Stop is observed again by a
	// fresh caller after the first connection/context has been closed.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+h.address+path, bytes.NewReader(data))
	publicFlowCheck(t, err)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: transport}).Do(req)
	publicFlowCheck(t, err)
	raw, err := io.ReadAll(res.Body)
	publicFlowCheck(t, errors.Join(err, res.Body.Close()))
	t.Logf("HTTP %s %s request=%s status=%d response=%s", method, path, data, res.StatusCode, raw)
	if out != nil {
		publicFlowCheck(t, json.Unmarshal(raw, out))
	}
	return res.StatusCode
}
func (h *publicFlowHost) want(t *testing.T, method, path string, body, out any, status int) {
	t.Helper()
	if got := h.request(t, method, path, body, out); got != status {
		t.Fatalf("%s %s: status %d want %d", method, path, got, status)
	}
}
func publicFlowAwait(t *testing.T, ctx context.Context, what string, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(what, ctx.Err())
		case <-ticker.C:
		}
	}
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
	f, err := os.OpenFile(js.Path(delivery), os.O_RDWR|os.O_APPEND, 0)
	publicFlowCheck(t, err)
	publicFlowCheck(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	// A separate descriptor proves the held inode excludes actual journal readers.
	sentinel, err := os.Open(js.Path(delivery))
	publicFlowCheck(t, err)
	err = syscall.Flock(int(sentinel.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("journal reader sentinel did not block: %v", err)
	}
	publicFlowCheck(t, sentinel.Close())
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
	publicFlowAwait(t, ctx, "inactive implementation and public gate", func() bool {
		s := h.state(t, a)
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
			publicFlowAwait(t, ctx, "A question and idle", func() bool {
				s := h.state(t, a)
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
