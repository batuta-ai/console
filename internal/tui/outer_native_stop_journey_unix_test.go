//go:build darwin || linux

package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

// These are external consumer qualifications. The private host supplies only
// synthetic credentials/capacity, FIFO workspaces and real journal locks. All
// mission mutations except catalog setup and stale-authority negative probes
// originate as keyboard input to the freshly built Console.
func TestOuterConsolePTYNativeStopRestartSHIP(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			j := newOuterNativeJourney(t, provider)
			a, b := j.catalog(true), j.catalog(false)
			j.launchConsole()
			a.approval = j.approve(a)
			b.approval = j.approve(b)
			if a.approval.ID == b.approval.ID || a.mission.ID == b.mission.ID || a.workspace.ID == b.workspace.ID || a.workspace.Root == b.workspace.Root || a.approval.Identity.Binding.CanonicalRoot == b.approval.Identity.Binding.CanonicalRoot {
				t.Fatal("two independent canonical workspaces/approvals required")
			}
			j.selectMission(a)
			j.execute(a, false)
			var first domain.ExecutionState
			j.host.awaitReadiness(t, j.ctx, a.approval, "A parked question", func(s domain.ExecutionState) bool {
				if s.Active || s.Watch == nil || s.Watch.PendingQuestion == nil {
					return false
				}
				first = s
				return true
			})
			question := *first.Watch.PendingQuestion
			q := question.Target
			if q.MissionID != a.mission.ID || q.WorkspaceID != a.workspace.ID || q.Delivery != first.Delivery || q.OpenedDigest != first.OpenedDigest || q.Task == "" || q.Execution < 1 || q.QuestionID == "" || question.Prompt != "exact native question?" {
				t.Fatal("incomplete public question tuple", question)
			}
			j.refresh(a)
			visibleQuestion := j.progress()
			if visibleQuestion.Watch.PendingQuestion == nil || !reflect.DeepEqual(*visibleQuestion.Watch.PendingQuestion, question) {
				t.Fatal("full rendered question differs from public tuple/prompt", visibleQuestion)
			}
			j.console.key(t, "j")
			j.console.contains(t, j.ctx, strconv.Quote(question.Prompt))
			beforeAnswer := publicFlowJournal(t, a.approval, first.Delivery)
			j.console.key(t, "exact native\ranswer\t\r")
			j.visibleJSON("Confirm: Record this answer; resume remains separate", q.Answer("exact native\nanswer"))
			j.console.key(t, "\r")
			j.console.contains(t, j.ctx, "Last verified effect: recorded")
			answered := j.host.state(t, a.approval)
			afterAnswer := publicFlowJournal(t, a.approval, first.Delivery)
			if answered.Active || answered.RunID != "" || len(afterAnswer) != len(beforeAnswer)+1 || afterAnswer[len(afterAnswer)-1].Kind != loop.KindAnswer || !reflect.DeepEqual(beforeAnswer, afterAnswer[:len(beforeAnswer)]) {
				t.Fatal("keyboard answer did not record exactly once without launching", answered)
			}
			var recorded struct {
				Execution int    `json:"execution"`
				Answer    string `json:"answer"`
				Question  string `json:"question"`
			}
			publicFlowCheck(t, json.Unmarshal(afterAnswer[len(afterAnswer)-1].Detail, &recorded))
			if afterAnswer[len(afterAnswer)-1].TaskID != q.Task || recorded.Execution != q.Execution || recorded.Answer != "exact native\nanswer" || recorded.Question != question.Prompt {
				t.Fatal("recorded answer bytes/task/execution differ from keyboard confirmation", recorded)
			}
			if _, err := os.Stat(filepath.Join(publicFlowControlDir(a.approval), "first.birth")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("answer launched provider", err)
			}
			j.refresh(a)
			j.execute(a, true)
			aRun := j.active(a)
			if aRun.Delivery != first.Delivery || aRun.OpenedDigest != first.OpenedDigest {
				t.Fatal("explicit resume changed original delivery/opening")
			}
			target := j.tree(a, "first")
			j.selectMission(b)
			j.refresh(b)
			j.execute(b, false)
			bRun := j.active(b)
			sibling := j.tree(b, "first")
			if aRun.RunID == bRun.RunID || aRun.Delivery == bRun.Delivery {
				t.Fatal("shared capacity admissions not independent")
			}
			foreign := j.foreign()
			j.selectMission(a)
			j.refresh(a)
			req := outerNativeStopReference(a.approval, aRun)
			j.prepareStop(req, false)
			_, unlock := publicFlowLock(t, a.approval, aRun.Delivery)
			heldAt := time.Now()
			j.console.key(t, "\r\r")
			j.pendingStop(req, 1)
			publicFlowRetired(t, j.ctx, target)
			publicFlowCoreBlocked(t, j.ctx, j.host)
			// Cancel the explicit refresh by navigating; never wait on a reader of
			// the locked journal or consume the production finalization budget.
			j.console.key(t, "z\rfm")
			j.selectMission(b)
			publicFlowAlive(t, append(sibling, foreign)...)
			j.selectMission(a)
			j.pendingStop(req, 1)
			j.prepareStop(req, true)
			j.console.key(t, "\r")
			j.pendingStop(req, 2)
			// Read the retained full outcomes after unlocking. Paging must never
			// spend Core's existing five-second finalization budget.
			unlock()
			j.releasedJournal(heldAt)
			publicFlowAlive(t, append(sibling, foreign)...)
			j.host.awaitReadiness(t, j.ctx, a.approval, "canceled backend inactive", func(s domain.ExecutionState) bool {
				return !s.Active && s.RunID == "" && s.Watch != nil && s.Watch.Header["state"] == loop.StateCanceled
			})
			j.refresh(a)
			j.stopOutcome(req, domain.ExecutionStopPending, 2)
			j.prepareStop(req, true)
			j.console.key(t, "\r")
			completed := j.stopOutcome(req, domain.ExecutionStopCompleted, 3)
			if completed.CoreState != loop.StateCanceled || completed.State == nil || completed.State.Active || completed.State.RunID != "" || completed.State.Watch == nil || completed.State.Watch.Header["state"] != loop.StateCanceled {
				t.Fatal("Stop did not expose actual durable cancellation", completed)
			}
			j.refresh(a)
			j.console.contains(t, j.ctx, "Completion is not verified")
			publicFlowRelease(t, b.approval, "first")
			j.ship(b, bRun)
			publicFlowRetired(t, j.ctx, sibling)
			aRecords := publicFlowJournal(t, a.approval, aRun.Delivery)
			bRecords := publicFlowJournal(t, b.approval, bRun.Delivery)
			j.original(a)
			j.original(b)
			oldHost, oldConsole := publicFlowObserve(t, j.host.cmd.Process.Pid), publicFlowObserve(t, j.console.cmd.Process.Pid)
			oldAddress, oldToken := j.host.address, j.host.token
			j.closeConsole()
			j.closeHost(j.host)
			if oldHost.alive(t) || oldConsole.alive(t) {
				t.Fatal("old host/Console incarnation survived joined restart")
			}
			publicFlowAlive(t, foreign)
			j.launchHost()
			if j.host.address == oldAddress || j.host.token != oldToken {
				t.Fatal("restart needs new address and same protected authentication")
			}
			j.launchConsole()
			j.selectMission(a)
			j.refresh(a)
			j.receipt(a)
			recovered := j.progress()
			if recovered.Active || recovered.RunID != "" || recovered.Delivery != first.Delivery || recovered.OpenedDigest != first.OpenedDigest || recovered.Watch == nil || recovered.Watch.Header["state"] != loop.StateCanceled {
				t.Fatal("rendered restart lost original canceled delivery", recovered)
			}
			j.ship(b, bRun)
			if !reflect.DeepEqual(aRecords, publicFlowJournal(t, a.approval, aRun.Delivery)) || !reflect.DeepEqual(bRecords, publicFlowJournal(t, b.approval, bRun.Delivery)) {
				t.Fatal("restart/read-only navigation mutated journals")
			}
			j.original(a)
			j.original(b)
			j.rejectOld(req)
			publicFlowWrite(t, filepath.Join(publicFlowControlDir(a.approval), "phase"), []byte("replacement\n"), 0600)
			j.selectMission(a)
			j.refresh(a)
			j.execute(a, true)
			next := j.active(a)
			if next.RunID == aRun.RunID || next.RunID == bRun.RunID || next.Delivery != first.Delivery || next.OpenedDigest != first.OpenedDigest {
				t.Fatal("replacement reused nonce or changed opening", next)
			}
			replacement := j.tree(a, "replacement")
			j.rejectOld(req)
			still := j.host.state(t, a.approval)
			if !still.Active || still.RunID != next.RunID {
				t.Fatal("old Stop controlled replacement", still)
			}
			publicFlowAlive(t, append(replacement, foreign)...)
			publicFlowRelease(t, a.approval, "replacement")
			j.ship(a, next)
			publicFlowRetired(t, j.ctx, replacement)
			j.original(a)
			j.original(b)
			j.closeConsole()
			j.closeHost(j.host)
		})
	}
}

func TestOuterConsolePTYNativeStopJournalFault(t *testing.T) {
	j := newOuterNativeJourney(t, "codex")
	a := j.catalog(false)
	j.launchConsole()
	a.approval = j.approve(a)
	j.execute(a, false)
	run := j.active(a)
	tree := j.tree(a, "first")
	foreign := j.foreign()
	j.refresh(a)
	req := outerNativeStopReference(a.approval, run)
	j.prepareStop(req, false)
	f, unlock := publicFlowLock(t, a.approval, run.Delivery)
	heldAt := time.Now()
	j.console.key(t, "\r")
	j.pendingStop(req, 1)
	publicFlowRetired(t, j.ctx, tree)
	publicFlowCoreBlocked(t, j.ctx, j.host)
	before, err := f.Stat()
	publicFlowCheck(t, err)
	_, err = f.WriteString("not-a-journal-record\n")
	publicFlowCheck(t, err)
	publicFlowCheck(t, f.Sync())
	after, err := os.Stat(f.Name())
	publicFlowCheck(t, err)
	if !os.SameFile(before, after) {
		t.Fatal("fault replaced journal inode")
	}
	unlock()
	j.releasedJournal(heldAt)
	// The disposable host has only this admission. Its real run goroutine must
	// finish before observing the fault; stacks confer no Stop/result authority.
	publicFlowAwait(t, j.ctx, "fault host native lifetime joined", func() bool {
		event := j.host.control(t, publicFlowControl{Op: "stacks"})
		return !strings.Contains(event.Stacks, "service.(*Service).runNativeExecution(")
	})
	// Each pending observation is an explicit y/Enter, never an HTTP substitute.
	count := 1
	publicFlowAwait(t, j.ctx, "rendered finalized journal uncertainty", func() bool {
		j.prepareStop(req, true)
		j.console.key(t, "\r")
		count++
		out := j.stopOutcome(req, "", count)
		if out.Effect == domain.ExecutionStopPending {
			return false
		}
		causes := strings.Join(out.Causes, "\n")
		if out.Effect != domain.ExecutionStopUncertain || !out.Finalized || out.Durable || out.CoreState != "" || out.Error == nil || !strings.Contains(causes, batuta.ErrArtifactInvalid.Error()) || !strings.Contains(causes, context.Canceled.Error()) {
			t.Fatal("rendered fault concealed uncertainty or independent causes", out)
		}
		return true
	})
	j.console.key(t, "z\r")
	j.selectMission(a)
	j.stopOutcome(req, domain.ExecutionStopUncertain, count)
	j.console.key(t, "f")
	j.console.contains(t, j.ctx, "Request failed; confirmed facts and draft retained")
	j.stopOutcome(req, domain.ExecutionStopUncertain, count)
	j.console.key(t, "z\r")
	j.selectMission(a)
	j.stopOutcome(req, domain.ExecutionStopUncertain, count)
	publicFlowAlive(t, foreign)
	j.original(a)
	j.closeConsole()
	j.closeHost(j.host)
}

type outerNativeMission struct {
	workspace domain.Workspace
	mission   domain.Mission
	approval  domain.PlanApproval
}

type outerNativeJourney struct {
	t             *testing.T
	ctx           context.Context
	root          string
	evidence      string
	cfg           publicFlowConfig
	binary        string
	host          *publicFlowHost
	hosts         []*publicFlowHost
	console       *outerJourneyProcess
	consoles      []*outerJourneyProcess
	births        []publicFlowBirth
	account       domain.Account
	foreignCmd    *exec.Cmd
	foreignEnd    bool
	negativeStops int
	stopCount     int
}

func newOuterNativeJourney(t *testing.T, provider string) *outerNativeJourney {
	t.Helper()
	root, err := os.MkdirTemp("", "console-outer-native-")
	publicFlowCheck(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	j := &outerNativeJourney{t: t, ctx: ctx, root: root, evidence: root}
	t.Logf("ARTIFACTS %s", root)
	t.Cleanup(func() {
		defer cancel()
		joined := true
		for i, p := range j.consoles {
			publicFlowWrite(t, filepath.Join(j.evidence, fmt.Sprintf("console-%d.pty", i)), []byte(p.output()), 0600)
			joined = joined && p.joined
		}
		for _, h := range j.hosts {
			joined = joined && h.joined && h.cmd.ProcessState != nil && h.cmd.ProcessState.Success()
		}
		for _, birth := range j.births {
			if birth.alive(t) {
				joined = false
				t.Errorf("owned incarnation survived cleanup: %+v", birth)
			}
		}
		joined = joined && (j.foreignCmd == nil || j.foreignEnd)
		t.Logf("JOIN consoles=%d hosts=%d provider_incarnations=%d foreign=%t verified=%t", len(j.consoles), len(j.hosts), len(j.births), j.foreignEnd, joined)
		publicFlowWrite(t, filepath.Join(j.evidence, "outcome.json"), publicFlowJSON(t, map[string]any{"joined": joined, "failed": t.Failed(), "runtime_root": root, "retained": !joined || t.Failed(), "consoles": len(j.consoles), "hosts": len(j.hosts), "provider_births": j.births, "foreign_joined": j.foreignEnd}), 0600)
		if !joined || t.Failed() {
			t.Logf("RETAIN %s (failure or uncertain completion)", root)
			return
		}
		publicFlowCheck(t, os.RemoveAll(root))
	})
	proofRoot := filepath.Join("..", "..", ".batuta", "runs", "outer-stop-proof")
	publicFlowCheck(t, os.MkdirAll(proofRoot, 0700))
	j.evidence, err = os.MkdirTemp(proofRoot, provider+"-")
	publicFlowCheck(t, err)
	t.Logf("EVIDENCE %s", j.evidence)
	j.cfg = publicFlowBuildAt(t, provider, root)
	j.cfg.ObserveStops = true
	publicFlowWrite(t, filepath.Join(j.evidence, "provenance.json"), publicFlowJSON(t, j.cfg), 0600)
	j.binary = filepath.Join(root, "consumer", "console")
	publicFlowCheck(t, os.MkdirAll(filepath.Dir(j.binary), 0700))
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", j.binary, "./cmd/console")
	cmd.Dir = filepath.Join("..", "..")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	t.Logf("BUILD argv=%q", cmd.Args)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Console consumer build: %v\n%s", err, raw)
	}
	publicFlowVerifyBinary(t, j.binary, publicFlowHash(t, j.binary), "github.com/batuta-ai/console/cmd/console")
	t.Logf("BUILD exit=%s consumer_sha256=%s qualification_source_sha256=%s", cmd.ProcessState, publicFlowHash(t, j.binary), publicFlowHash(t, "outer_native_stop_journey_unix_test.go"))
	exe, err := os.Executable()
	publicFlowCheck(t, err)
	publicFlowWrite(t, filepath.Join(j.evidence, "consumer.json"), publicFlowJSON(t, map[string]string{"console_sha256": publicFlowHash(t, j.binary), "qualification_source_sha256": publicFlowHash(t, "outer_native_stop_journey_unix_test.go"), "host_test_binary_sha256": publicFlowHash(t, exe), "host_observer_source_sha256": publicFlowHash(t, "public_flow_host_unix_test.go"), "observer_body_limit": strconv.Itoa(publicFlowStopBodyLimit), "observer_record_limit": strconv.Itoa(publicFlowStopRecordLimit), "go": runtime.Version(), "build_exit": cmd.ProcessState.String()}), 0600)
	for _, path := range []string{filepath.Join(filepath.Dir(j.binary), "consoled"), filepath.Join(j.cfg.Bin, "consoled")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("autostartable daemon present", path, err)
		}
	}
	j.launchHost()
	j.account = publicFlowAccount(t, j.host)
	return j
}

func (j *outerNativeJourney) launchHost() {
	j.t.Helper()
	h := publicFlowLaunch(j.t, j.cfg)
	j.host, j.hosts = h, append(j.hosts, h)
	j.negativeStops = 0
	j.observedStops(0)
	index := len(j.hosts) - 1
	// Preserve the host's own log before its approved helper removes TempDir.
	j.t.Cleanup(func() {
		j.closeHost(h)
		for _, env := range h.cmd.Env {
			if path, ok := strings.CutPrefix(env, "BATUTA_TEST_PUBLIC_FLOW_CONFIG="); ok {
				raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "host.log"))
				publicFlowCheck(j.t, err)
				publicFlowWrite(j.t, filepath.Join(j.evidence, fmt.Sprintf("host-%d.log", index)), raw, 0600)
			}
		}
	})
	info, err := os.Stat(filepath.Join(j.cfg.Home, "auth.secret"))
	publicFlowCheck(j.t, err)
	if info.Mode().Perm() != 0600 || h.token == "" {
		j.t.Fatal("missing protected shared auth")
	}
	var capabilities json.RawMessage
	h.want(j.t, "GET", "/v0/capabilities", nil, &capabilities, 200)
	j.t.Logf("HOST pid=%d address=%s authenticated capabilities; private readiness/capacity=2; no host provider keys", h.cmd.Process.Pid, h.address)
}

func (j *outerNativeJourney) closeHost(h *publicFlowHost) {
	j.t.Helper()
	if !h.joined && !j.t.Failed() {
		if h == j.host {
			j.observedStops(j.stopCount)
		}
		event := h.control(j.t, publicFlowControl{Op: "stop_observations"})
		publicFlowWrite(j.t, filepath.Join(j.evidence, fmt.Sprintf("host-%d-stop-observations.json", h.cmd.Process.Pid)), publicFlowJSON(j.t, event), 0600)
	}
	h.close(j.t)
	if h.cmd.ProcessState == nil || !h.cmd.ProcessState.Success() {
		j.t.Errorf("host drain not proven: %v", h.cmd.ProcessState)
	}
}

func (j *outerNativeJourney) launchConsole() {
	j.t.Helper()
	cmd := exec.Command(j.binary, "-home", j.cfg.Home, "-listen", j.host.address)
	cmd.Dir = j.root
	cmd.Env = []string{"HOME=" + j.cfg.Home, "PATH=" + j.cfg.Bin, "TMPDIR=" + j.root, "TERM=xterm-256color", "LANG=C", "BATUTA_LANG=en", "XDG_CONFIG_HOME=" + filepath.Join(j.root, "config"), "XDG_DATA_HOME=" + filepath.Join(j.root, "data"), "CODEX_HOME=" + filepath.Join(j.root, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(j.root, "claude")}
	j.console = outerJourneyStart(j.t, cmd, true)
	j.consoles = append(j.consoles, j.console)
	j.console.contains(j.t, j.ctx, "w workspaces (select / create / setup)")
}

func (j *outerNativeJourney) closeConsole() {
	j.t.Helper()
	p := j.console
	p.key(j.t, "q")
	p.join(j.t)
	publicFlowCheck(j.t, p.pty.Close())
	select {
	case <-p.readerDone:
		p.joined = true
	case <-time.After(5 * time.Second):
		j.t.Fatal("Console reader failed to join; artifacts retained")
	}
	j.t.Logf("JOIN Console pid=%d process and PTY reader", p.cmd.Process.Pid)
}

func (j *outerNativeJourney) catalog(ask bool) outerNativeMission {
	j.t.Helper()
	event := j.host.control(j.t, publicFlowControl{Op: "workspace", Ask: ask})
	var fixture struct{ Root, Plan, Path string }
	publicFlowCheck(j.t, json.Unmarshal(publicFlowJSON(j.t, event.Data), &fixture))
	if event.Kind != "workspace" || fixture.Plan != publicFlowPlan || fixture.Path != publicFlowPlanPath {
		j.t.Fatal("workspace fixture protocol drift", event)
	}
	var m outerNativeMission
	j.host.want(j.t, "POST", "/v0/workspaces", map[string]string{"name": "Outer native", "root": fixture.Root}, &m.workspace, 200)
	model := map[string]string{"codex": "qa-native", "claude": "sonnet"}[j.cfg.Provider]
	var squad domain.Squad
	j.host.want(j.t, "POST", "/v0/squads", domain.Squad{Name: "Outer native", Members: []domain.Member{{Role: "lead", CLI: j.cfg.Provider, Model: model, AccountID: j.account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}}, &squad, 200)
	j.host.want(j.t, "POST", "/v0/missions", map[string]string{"workspace_id": m.workspace.ID, "title": "Outer native", "brief": "Exact " + m.workspace.ID, "squad_id": squad.ID}, &m.mission, 200)
	if m.mission.Binding != nil {
		j.t.Fatal("catalog seeded binding")
	}
	return m
}

func (j *outerNativeJourney) selectMission(m outerNativeMission) {
	j.t.Helper()
	j.console.key(j.t, "m")
	s := j.console.screen(j.t, j.ctx, "mission/workspace catalog", func(s string) bool {
		return strings.Contains(s, "OBJECTIVE") && strings.Contains(s, m.mission.ID+"  "+m.workspace.ID)
	})
	marked := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "> ") {
				return strings.TrimSpace(line)
			}
		}
		return ""
	}
	if !strings.Contains(marked(s), m.mission.ID+"  "+m.workspace.ID) {
		j.console.key(j.t, "\x1b[B")
		j.console.screen(j.t, j.ctx, "exact keyboard selection", func(s string) bool {
			return strings.Contains(marked(s), m.mission.ID+"  "+m.workspace.ID)
		})
	}
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, m.mission.ID+"  Workspace: "+m.workspace.ID)
}

func (j *outerNativeJourney) approve(m outerNativeMission) domain.PlanApproval {
	j.t.Helper()
	j.selectMission(m)
	j.console.key(j.t, "b")
	j.console.contains(j.t, j.ctx, "Exact existing plan path")
	j.console.key(j.t, publicFlowPlanPath+"\t\t\r")
	j.visibleJSON("Confirm: Bind an existing exact plan", struct {
		Workspace string `json:"workspace_id"`
		Path      string `json:"plan_path"`
		Delivery  string `json:"delivery_id"`
	}{m.workspace.ID, publicFlowPlanPath, ""})
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, "Last verified effect: recorded")
	var detail domain.PlanDetail
	base := "/v0/missions/" + m.mission.ID
	j.host.want(j.t, "GET", base+"/plan?workspace_id="+m.workspace.ID, nil, &detail, 200)
	canonical, err := filepath.EvalSymlinks(m.workspace.Root)
	publicFlowCheck(j.t, err)
	binding := detail.Identity.Binding
	if detail.Identity.MissionID != m.mission.ID || binding.WorkspaceID != m.workspace.ID || binding.WorkspaceRoot != m.workspace.Root || binding.CanonicalRoot != canonical || binding.PlanPath != publicFlowPlanPath || binding.PlanSlug != "alpha" || binding.PlanDigest == "" || binding.DeliveryID != "" || binding.OpenedDigest != "" || detail.Approval != nil || string(detail.Content) != publicFlowPlan || detail.Identity.Version != domain.PlanVersion(detail.Content) {
		j.t.Fatal("bound identity/source differs", detail)
	}
	j.console.key(j.t, "o")
	outerJourneyReviewSource(j.t, j.ctx, j.console, detail)
	j.console.key(j.t, "y")
	j.visibleJSON("Confirm: Approve all inspected original source bytes", detail.Identity)
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, "Immutable approved snapshot")
	var approved domain.PlanDetail
	j.host.want(j.t, "GET", base+"/plan?workspace_id="+m.workspace.ID, nil, &approved, 200)
	if approved.Approval == nil || approved.Approval.Identity != detail.Identity || !bytes.Equal(approved.Approval.Content, detail.Content) || approved.Approval.ID == "" || approved.Approval.ApprovedAt.IsZero() {
		j.t.Fatal("keyboard approval receipt mismatch", approved)
	}
	j.console.contains(j.t, j.ctx, "Immutable approved snapshot "+approved.Approval.ID+" "+detail.Identity.Version)
	m.approval = *approved.Approval
	j.receipt(m)
	j.original(m)
	return m.approval
}

func (j *outerNativeJourney) original(m outerNativeMission) {
	j.t.Helper()
	raw, err := os.ReadFile(filepath.Join(m.workspace.Root, publicFlowPlanPath))
	publicFlowCheck(j.t, err)
	if !bytes.Equal(raw, m.approval.Content) || string(raw) != publicFlowPlan {
		j.t.Fatal("original source changed")
	}
	var got domain.PlanApproval
	j.host.want(j.t, "GET", publicFlowBase(m.approval)+"/plan/approvals/"+m.approval.ID+"?workspace_id="+m.workspace.ID, nil, &got, 200)
	if !reflect.DeepEqual(got, m.approval) {
		j.t.Fatal("immutable receipt changed", got)
	}
}

func (j *outerNativeJourney) refresh(m outerNativeMission) {
	j.t.Helper()
	ctx, cancel := context.WithTimeout(j.ctx, 10*time.Second)
	defer cancel()
	for {
		j.console.mu.Lock()
		before := j.console.raw.Len()
		j.console.mu.Unlock()
		j.console.key(j.t, "f")
		s := j.console.screen(j.t, ctx, "explicit refreshed "+m.mission.ID, func(s string) bool {
			j.console.mu.Lock()
			changed := j.console.raw.Len() > before
			j.console.mu.Unlock()
			if !changed || !strings.Contains(s, m.mission.ID+"  Workspace: "+m.workspace.ID) || strings.Contains(s, "Confirm:") {
				return false
			}
			return strings.Contains(s, "Request failed") || (strings.Contains(s, "Last verified effect: read_only") && strings.Contains(s, "Immutable approved snapshot "+m.approval.ID+" "+m.approval.Identity.Version) && !strings.Contains(s, "refresh required"))
		})
		if !strings.Contains(s, "Request failed") {
			return
		}
		if !strings.Contains(s, "plan_io") {
			j.t.Fatal("unexpected explicit inspection failure", s)
		}
		publicFlowCheck(j.t, context.Cause(ctx))
	}
}

func (j *outerNativeJourney) execute(m outerNativeMission, resume bool) {
	j.t.Helper()
	j.refresh(m)
	key, label := "e", "Start native execution from this receipt"
	if resume {
		key, label = "g", "Resume the original delivery and opening"
	}
	j.console.key(j.t, key)
	if resume {
		s := j.host.state(j.t, m.approval)
		j.visibleJSON("Confirm: "+label, domain.ResumeRequest{Target: m.approval.Identity, ApprovalID: m.approval.ID, Delivery: s.Delivery, OpenedDigest: s.OpenedDigest})
	} else {
		j.visibleJSON("Confirm: "+label, domain.ExecutionRequest{Target: m.approval.Identity, ApprovalID: m.approval.ID})
	}
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, "Last verified effect: started")
}

func (j *outerNativeJourney) active(m outerNativeMission) domain.ExecutionState {
	j.t.Helper()
	var state domain.ExecutionState
	j.host.awaitReadiness(j.t, j.ctx, m.approval, "held active admission", func(s domain.ExecutionState) bool {
		if !s.Active || s.Watch == nil {
			return false
		}
		if !domain.ValidExecutionRunID(s.RunID) || s.OpenedDigest == "" || !s.Bound {
			j.t.Fatal("invalid admission", s)
		}
		state = s
		return true
	})
	return state
}

func (j *outerNativeJourney) tree(m outerNativeMission, phase string) []publicFlowBirth {
	j.t.Helper()
	tree := publicFlowTree(j.t, j.ctx, m.approval, phase)
	j.births = append(j.births, tree...)
	return tree
}

func (j *outerNativeJourney) foreign() publicFlowBirth {
	j.t.Helper()
	cmd := exec.Command("/bin/sleep", "300")
	publicFlowCheck(j.t, cmd.Start())
	j.foreignCmd = cmd
	birth := publicFlowObserve(j.t, cmd.Process.Pid)
	j.t.Cleanup(func() {
		publicFlowCheck(j.t, cmd.Process.Kill())
		err := cmd.Wait()
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			publicFlowCheck(j.t, err)
		}
		j.foreignEnd = true
		if birth.alive(j.t) {
			j.t.Error("foreign fixture incarnation survived Wait")
		}
		j.t.Logf("JOIN owned foreign pid=%d status=%s", cmd.Process.Pid, cmd.ProcessState)
	})
	return birth
}

func outerNativeStopReference(a domain.PlanApproval, s domain.ExecutionState) domain.ExecutionStopRequest {
	return domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: s.Delivery, RunID: s.RunID}
}

func (j *outerNativeJourney) prepareStop(req domain.ExecutionStopRequest, observe bool) {
	j.t.Helper()
	key, label := "z", "Stop this exact native delivery attempt"
	if observe {
		key, label = "y", "Reobserve Stop using the same exact request"
	}
	j.console.key(j.t, key)
	j.visibleJSON("Confirm: "+label, req)
	j.observedStops(j.stopCount)
}

func (j *outerNativeJourney) visibleJSON(title string, want any) {
	j.t.Helper()
	outerInspectionReviewJSON(j.t, j.ctx, j.console, title, want)
}

func (j *outerNativeJourney) receipt(m outerNativeMission) {
	j.t.Helper()
	j.console.key(j.t, "k")
	j.visibleJSON("Immutable approved snapshot", m.approval)
	j.console.key(j.t, "\x1b")
	j.console.contains(j.t, j.ctx, m.mission.ID+"  Workspace: "+m.workspace.ID)
	j.refresh(m)
}

func (j *outerNativeJourney) observedStops(count int) publicFlowStopSnapshot {
	j.t.Helper()
	var snapshot publicFlowStopSnapshot
	outerJourneyAwait(j.t, j.ctx, "server-boundary Stop dispatch completion", func() bool {
		event := j.host.control(j.t, publicFlowControl{Op: "stop_observations"})
		if event.Kind != "stop_observations" {
			j.t.Fatal("Stop observer protocol", event.Kind)
		}
		publicFlowCheck(j.t, json.Unmarshal(publicFlowJSON(j.t, event.Data), &snapshot))
		if !snapshot.Enabled || snapshot.Overflow || snapshot.Count > count || len(snapshot.Records) > count {
			j.t.Fatal("unexpected server-boundary Stop dispatch/observer overflow", snapshot, count)
		}
		return snapshot.Count == count && snapshot.InFlight == 0 && len(snapshot.Records) == count
	})
	j.stopCount = count
	return snapshot
}

func (j *outerNativeJourney) stopDispatch(req domain.ExecutionStopRequest, count, status int) {
	j.t.Helper()
	snapshot := j.observedStops(count)
	for i, record := range snapshot.Records {
		body := publicFlowJSON(j.t, req)
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		if record.Sequence != i+1 || record.Path != publicFlowBase(domain.PlanApproval{Identity: req.Target})+"/execution/stop" || record.Request == nil || *record.Request != req || record.Truncated || record.BodyBytes != len(body) || record.BodySHA256 != digest {
			j.t.Fatal("server-boundary Stop body/reference mismatch", record, req)
		}
		wantStatus := http.StatusAccepted
		if i == count-1 {
			wantStatus = status
		}
		if record.Status != wantStatus {
			j.t.Fatal("server-boundary Stop status", record, wantStatus)
		}
	}
	j.t.Logf("SERVER-BOUNDARY HTTP Stop count=%d status=%d request=%s (not raw packet capture)", count, status, publicFlowJSON(j.t, req))
}

func (j *outerNativeJourney) pendingStop(req domain.ExecutionStopRequest, count int) {
	j.t.Helper()
	j.stopDispatch(req, count, http.StatusAccepted)
	j.console.screen(j.t, j.ctx, "rendered pending native Stop", func(s string) bool {
		return !strings.Contains(s, "Confirm:") && strings.Contains(s, "Cancellation pending; y explicitly reobserve the same attempt")
	})
}

func (j *outerNativeJourney) releasedJournal(heldAt time.Time) {
	j.t.Helper()
	elapsed := time.Since(heldAt)
	j.t.Logf("FIXTURE journal unlocked after %s; full retained history paging follows unlock", elapsed)
}

func (j *outerNativeJourney) stopOutcome(req domain.ExecutionStopRequest, effect domain.ExecutionStopEffect, count int) domain.ExecutionStopResult {
	j.t.Helper()
	snapshot := j.observedStops(count)
	status := snapshot.Records[count-1].Status
	label := map[int]string{
		202: "Cancellation pending; y explicitly reobserve the same attempt",
		200: "Stop finalized durably; actual Core state follows",
		503: "Stop outcome uncertain; no automatic retry; y explicitly reobserve",
	}[status]
	if label == "" {
		j.t.Fatal("unexpected actual Stop status", status)
	}
	j.console.screen(j.t, j.ctx, "rendered Stop response", func(s string) bool {
		return !strings.Contains(s, "Confirm:") && strings.Contains(s, label)
	})
	if status == http.StatusOK {
		j.refresh(outerNativeMission{mission: domain.Mission{ID: req.Target.MissionID}, workspace: domain.Workspace{ID: req.Target.Binding.WorkspaceID}, approval: domain.PlanApproval{ID: req.ApprovalID, Identity: req.Target}})
	} else if status == http.StatusServiceUnavailable {
		j.console.key(j.t, "f")
		j.console.contains(j.t, j.ctx, "Request failed; confirmed facts and draft retained")
	}
	visible := j.inspection()
	if visible.Mission != req.Target.MissionID || visible.Workspace != req.Target.Binding.WorkspaceID || visible.Pending != nil || len(visible.Stops) != count {
		j.t.Fatal("retained Stop history/identity/pending request mismatch", visible)
	}
	if status == http.StatusServiceUnavailable && (visible.Fresh || visible.Problem == "" || visible.Error == "") {
		j.t.Fatal("fault inspection falsely restored current authority", visible)
	}
	var latest domain.ExecutionStopResult
	for i, stop := range visible.Stops {
		if stop.Request == nil || *stop.Request != req || stop.Result == nil || stop.Result.ExecutionStopRequest != req || stop.Latest != (i == count-1) {
			j.t.Fatal("rendered Stop retargeted/lost latest observation", stop)
		}
		current := visible.Execution != nil && visible.Execution.Target == req.Target && visible.Execution.ApprovalID == req.ApprovalID && visible.Execution.Delivery == req.Delivery && visible.Execution.RunID == req.RunID
		if stop.MatchesCurrent != current {
			j.t.Fatal("rendered attempt match is false", stop, visible.Execution)
		}
		out := *stop.Result
		if stop.Effect != string(out.Effect) {
			j.t.Fatal("transport effect differs from actual Stop outcome", stop)
		}
		switch out.Effect {
		case domain.ExecutionStopPending:
			if out.Finalized || out.Durable || out.Error != nil || out.State != nil || out.CoreState != "" || len(out.Causes) != 0 || stop.Error != "" {
				j.t.Fatal("pending falsely finalized", stop)
			}
		case domain.ExecutionStopCompleted:
			if !out.Finalized || !out.Durable || out.Error != nil || stop.Error != "" {
				j.t.Fatal("completion lacks durable authority", stop)
			}
		case domain.ExecutionStopUncertain:
			if !out.Finalized || out.Error == nil || out.Error.Code == "" || stop.Error == "" {
				j.t.Fatal("uncertainty lost actual error", stop)
			}
		default:
			j.t.Fatal("unexpected rendered Stop outcome", stop)
		}
		if i < count-1 && out.Effect != domain.ExecutionStopPending {
			j.t.Fatal("earlier pending history changed", stop)
		}
		latest = out
	}
	if effect != "" && latest.Effect != effect {
		j.t.Fatal("rendered Stop effect", latest, effect)
	}
	expectedStatus := map[domain.ExecutionStopEffect]int{domain.ExecutionStopPending: 202, domain.ExecutionStopCompleted: 200, domain.ExecutionStopUncertain: 503}[latest.Effect]
	j.stopDispatch(req, count, expectedStatus)
	j.t.Logf("RENDERED STOP response=%s", publicFlowJSON(j.t, latest))
	return latest
}

func (j *outerNativeJourney) rejectOld(req domain.ExecutionStopRequest) {
	j.t.Helper()
	before := j.observedStops(j.negativeStops)
	out, status := j.host.stop(j.t, req)
	j.negativeStops++
	after := j.observedStops(j.negativeStops)
	record := after.Records[len(before.Records)]
	if record.Status != status || record.Request == nil || *record.Request != req {
		j.t.Fatal("disclosed negative old-nonce probe differs at handler boundary", record)
	}
	j.t.Logf("NEGATIVE old-nonce HTTP probe=%d status=%d request=%s", j.negativeStops, status, publicFlowJSON(j.t, req))
	if status != 400 || out.Effect != domain.ExecutionStopRejected || out.Finalized || out.Durable {
		j.t.Fatal("old RunID gained authority", status, out)
	}
}

func (j *outerNativeJourney) ship(m outerNativeMission, run domain.ExecutionState) {
	j.t.Helper()
	// Real source/Core inspection is fixture evidence, separately from the TUI.
	publicFlowShip(j.t, j.ctx, j.host, m.approval, domain.ExecutionResult{Delivery: run.Delivery, OpenedDigest: run.OpenedDigest})
	j.selectMission(m)
	j.refresh(m)
	j.console.contains(j.t, j.ctx, "Done - current cleared gate and evidence")
	j.receipt(m)
	visible := j.progress()
	if visible.Target != m.approval.Identity || visible.ApprovalID != m.approval.ID || visible.Active || visible.RunID != "" || visible.Delivery != run.Delivery || visible.OpenedDigest != run.OpenedDigest || visible.Watch == nil || visible.Watch.Source != domain.SourceCurrent || visible.Watch.Header["state"] != loop.StateDone || visible.Gate == nil || !visible.Gate.Required || !visible.Gate.Cleared || visible.Gate.EvidenceDigest == "" {
		j.t.Fatal("rendered progress is not actual SHIP", visible)
	}
	actual := j.host.state(j.t, m.approval)
	if !outerNativeExecutionEqual(visible, actual) {
		j.t.Fatal("rendered progress differs from public backend", visible, actual)
	}
}

type outerNativeStopView struct {
	Latest         bool                         `json:"latest_observation"`
	MatchesCurrent bool                         `json:"matches_current_attempt"`
	Request        *domain.ExecutionStopRequest `json:"request"`
	Result         *domain.ExecutionStopResult  `json:"result"`
	Effect         string                       `json:"transport_effect"`
	Error          string                       `json:"error"`
}

type outerNativeProgress struct {
	Mission   string                       `json:"mission"`
	Workspace string                       `json:"workspace"`
	Execution *domain.ExecutionState       `json:"execution"`
	Watch     domain.WatchView             `json:"watch"`
	Fresh     bool                         `json:"fresh"`
	Uncertain string                       `json:"unresolved_mutation"`
	Problem   string                       `json:"problem"`
	Error     string                       `json:"problem_error"`
	Pending   *domain.ExecutionStopRequest `json:"pending_stop"`
	Stops     []outerNativeStopView        `json:"stops"`
}

func (j *outerNativeJourney) inspection() outerNativeProgress {
	j.t.Helper()
	j.console.key(j.t, "w")
	raw, _ := outerInspectionRead(j.t, j.ctx, j.console, "Progress snapshot (Esc, f, w refresh) / retained Stop observations", "")
	var visible outerNativeProgress
	publicFlowCheck(j.t, json.Unmarshal(raw, &visible))
	publicFlowWrite(j.t, filepath.Join(j.evidence, fmt.Sprintf("progress-%d-%d.json", len(j.consoles), len(j.console.output()))), raw, 0600)
	j.console.key(j.t, "\x1b")
	j.console.contains(j.t, j.ctx, visible.Mission+"  Workspace: "+visible.Workspace)
	return visible
}

func (j *outerNativeJourney) progress() domain.ExecutionState {
	j.t.Helper()
	visible := j.inspection()
	if visible.Execution == nil || visible.Mission != visible.Execution.Target.MissionID || visible.Workspace != visible.Execution.Target.Binding.WorkspaceID || visible.Execution.Watch == nil || !reflect.DeepEqual(visible.Watch, *visible.Execution.Watch) || !visible.Fresh || visible.Uncertain != "" || visible.Problem != "" || visible.Error != "" || visible.Pending != nil {
		j.t.Fatal("progress identity/watch/freshness mismatch", visible)
	}
	state := *visible.Execution
	// Esc leaves inspection and invalidates freshness; recover through the
	// actual keyboard refresh before the next answer/resume action.
	j.refresh(outerNativeMission{mission: domain.Mission{ID: state.Target.MissionID}, workspace: domain.Workspace{ID: state.Target.Binding.WorkspaceID}, approval: domain.PlanApproval{ID: state.ApprovalID, Identity: state.Target}})
	return state
}

// These controls exercise the original API's auth/body rejection without a
// socket or fabricated HTTP reply; they do not qualify the positive PTY flow.
func TestOuterNativeStopObserverBoundary(t *testing.T) {
	original := (&api.Server{Token: "private-observer-fixture"}).Handler()
	disabled, absent := publicFlowObserveStops(original, false)
	if absent != nil || reflect.ValueOf(disabled).Pointer() != reflect.ValueOf(original).Pointer() || absent.snapshot().Enabled {
		t.Fatal("default-off observer changed original handler")
	}
	serve := func(handler http.Handler, method, path, body string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authorized {
			req.Header.Set("Authorization", "Bearer private-observer-fixture")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	path := "/v0/missions/observer/execution/stop"
	for _, tc := range []struct {
		name, body string
		authorized bool
		status     int
	}{
		{"auth-does-not-consume-body", `{"secret":"must not be retained"}`, false, 401},
		{"exact-original-rejection", `{}`, true, 400},
		{"unknown-body-not-disclosed", `{"secret":"must not be retained"}`, true, 400},
		{"bounded-body", `{"secret":"` + strings.Repeat("x", publicFlowStopBodyLimit*2) + `"}`, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, observer := publicFlowObserveStops(original, true)
			baseline := serve(disabled, "POST", path, tc.body, tc.authorized)
			observed := serve(handler, "POST", path, tc.body, tc.authorized)
			if baseline.Code != tc.status || observed.Code != baseline.Code || !reflect.DeepEqual(observed.Header(), baseline.Header()) || observed.Body.String() != baseline.Body.String() {
				t.Fatal("observer altered original API response", observed, baseline)
			}
			snapshot := observer.snapshot()
			if !snapshot.Enabled || snapshot.Count != 1 || snapshot.InFlight != 0 || snapshot.Overflow || len(snapshot.Records) != 1 {
				t.Fatal("handler completion/count not observed", snapshot)
			}
			record := snapshot.Records[0]
			if record.Sequence != 1 || record.Path != path || record.Status != tc.status || record.Request != nil {
				t.Fatal("unexpected body/status record", record)
			}
			if !tc.authorized && record.BodyBytes != 0 {
				t.Fatal("observer eagerly consumed unauthenticated body", record)
			}
			if tc.name == "bounded-body" && (!record.Truncated || record.BodyBytes != publicFlowStopBodyLimit+1) {
				t.Fatal("original MaxBytesReader limit not preserved", record)
			}
			raw := string(publicFlowJSON(t, snapshot))
			if strings.Contains(raw, "secret") || strings.Contains(raw, "private-observer-fixture") {
				t.Fatal("observer disclosed arbitrary body or auth")
			}
			serve(handler, "GET", "/v0/capabilities", "", true)
			if !reflect.DeepEqual(snapshot, observer.snapshot()) {
				t.Fatal("non-Stop route was observed")
			}
		})
	}
	// A well-formed reference with a different route mission is rejected by the
	// real API before Service access, while retaining the exact consumed body.
	identity := domain.PlanIdentity{MissionID: "different-mission", Version: domain.PlanVersion([]byte("original source")), Binding: domain.MissionBinding{WorkspaceID: "workspace", WorkspaceRoot: "/owned", CanonicalRoot: "/owned", PlanPath: ".batuta/plans/alpha.md", PlanSlug: "alpha", PlanDigest: "task-digest"}}
	request := domain.ExecutionStopRequest{Target: identity, ApprovalID: "receipt", Delivery: domain.ExecutionDelivery("receipt", identity), RunID: strings.Repeat("a", 64)}
	publicFlowCheck(t, request.Validate())
	payload := string(publicFlowJSON(t, request))
	typedHandler, typedObserver := publicFlowObserveStops(original, true)
	res := serve(typedHandler, "POST", path, payload, true)
	typed := typedObserver.snapshot().Records[0]
	if res.Code != 400 || typed.Status != res.Code || typed.Request == nil || *typed.Request != request || typed.BodyBytes != len(payload) || typed.BodySHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(payload))) || typed.Truncated {
		t.Fatal("consumed reference/body digest differs at original API boundary", typed)
	}
	handler, observer := publicFlowObserveStops(original, true)
	for i := 0; i < publicFlowStopRecordLimit+1; i++ {
		serve(handler, "POST", path, `{}`, true)
	}
	snapshot := observer.snapshot()
	if snapshot.Count != publicFlowStopRecordLimit+1 || snapshot.InFlight != 0 || !snapshot.Overflow || len(snapshot.Records) != publicFlowStopRecordLimit {
		t.Fatal("observer record bound not enforced", snapshot)
	}
	writer := &publicFlowStopWriter{ResponseWriter: httptest.NewRecorder()}
	publicFlowCheck(t, http.NewResponseController(writer).Flush())
	if writer.Unwrap() == nil {
		t.Fatal("response controller cannot reach original writer")
	}
}

func outerNativeExecutionEqual(visible, actual domain.ExecutionState) bool {
	// Consecutive public reads compute age at different instants. Preserve all
	// raw evidence and compare every field except this validated duration.
	withoutAge := func(state domain.ExecutionState) (domain.ExecutionState, bool) {
		if state.Watch == nil {
			return state, false
		}
		age, ok := state.Watch.Health["journal_age_ns"].(float64)
		if !ok || age < 0 || math.IsNaN(age) || math.IsInf(age, 0) || math.Trunc(age) != age {
			return state, false
		}
		watch := *state.Watch
		watch.Health = maps.Clone(watch.Health)
		delete(watch.Health, "journal_age_ns")
		state.Watch = &watch
		return state, true
	}
	left, leftOK := withoutAge(visible)
	right, rightOK := withoutAge(actual)
	return leftOK && rightOK && reflect.DeepEqual(left, right)
}
