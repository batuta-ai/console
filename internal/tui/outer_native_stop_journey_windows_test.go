//go:build windows

package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
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
	"github.com/batuta-ai/console/internal/terminalengine"
	"github.com/batuta-ai/core/loop"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/windows"
)

// Catalog setup and stale-nonce negatives use public HTTP. All positive mission
// actions use the actual built Console keyboard. Private controls only supply
// synthetic providers/authentication, native witnesses, and real journal locks.
type winOuterNativeMission struct {
	workspace domain.Workspace
	mission   domain.Mission
	approval  domain.PlanApproval
}

type winOuterNativeJourney struct {
	t                         *testing.T
	ctx                       context.Context
	cfg                       winFlowConfig
	host                      *winFlowHTTP
	account                   domain.Account
	harness                   *windowsOuterHarness
	console                   *windowsOuterConsole
	stopCount, snapshotNumber int
}

func (j *winOuterNativeJourney) catalog(ask bool) winOuterNativeMission {
	j.t.Helper()
	root := j.host.workspace(j.t, j.ctx, ask)
	var m winOuterNativeMission
	j.host.want(j.t, "POST", "/v0/workspaces", map[string]string{"name": "Outer native", "root": root}, &m.workspace, 200)
	model := map[string]string{"codex": "qa-native", "claude": "sonnet"}[j.cfg.Provider]
	var squad domain.Squad
	j.host.want(j.t, "POST", "/v0/squads", domain.Squad{Name: "Outer native", Members: []domain.Member{{Role: "lead", CLI: j.cfg.Provider, Model: model, AccountID: j.account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}}, &squad, 200)
	j.host.want(j.t, "POST", "/v0/missions", map[string]string{"workspace_id": m.workspace.ID, "title": "Outer native", "brief": "Exact " + m.workspace.ID, "squad_id": squad.ID}, &m.mission, 200)
	if m.mission.Binding != nil {
		j.t.Fatal("catalog seeded binding")
	}
	return m
}

func (j *winOuterNativeJourney) selectMission(m winOuterNativeMission) {
	j.t.Helper()
	j.console.key(j.t, "m")
	s := j.console.screen(j.t, j.ctx, "mission/workspace catalog", func(s string) bool {
		return strings.Contains(s, "OBJECTIVE") && strings.Contains(s, m.mission.ID)
	})
	j.console.selectRow(j.t, j.ctx, s, "exact mission selection", m.mission.ID)
	j.console.key(j.t, "\r")
	j.normal()
	visible := j.inspection()
	if visible.Mission != m.mission.ID || visible.Workspace != m.workspace.ID {
		j.t.Fatal("paged mission selection differs from selected catalog row", visible)
	}
}

func (j *winOuterNativeJourney) approve(m winOuterNativeMission) domain.PlanApproval {
	j.t.Helper()
	j.selectMission(m)
	j.console.key(j.t, "b")
	j.console.contains(j.t, j.ctx, "Exact existing plan path")
	j.console.key(j.t, winFlowPlanPath+"\t\t\r")
	j.visibleJSON("Confirm: Bind an existing exact plan", struct {
		Workspace string `json:"workspace_id"`
		Path      string `json:"plan_path"`
		Delivery  string `json:"delivery_id"`
	}{m.workspace.ID, winFlowPlanPath, ""})
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, "Last verified effect: recorded")
	var detail domain.PlanDetail
	base := "/v0/missions/" + m.mission.ID
	j.host.want(j.t, "GET", base+"/plan?workspace_id="+m.workspace.ID, nil, &detail, 200)
	canonical, err := filepath.EvalSymlinks(m.workspace.Root)
	winFlowCheck(j.t, err)
	binding := detail.Identity.Binding
	if detail.Identity.MissionID != m.mission.ID || binding.WorkspaceID != m.workspace.ID || binding.WorkspaceRoot != m.workspace.Root || binding.CanonicalRoot != canonical || binding.PlanPath != winFlowPlanPath || binding.PlanSlug != "alpha" || binding.PlanDigest == "" || binding.DeliveryID != "" || binding.OpenedDigest != "" || detail.Approval != nil || string(detail.Content) != winFlowPlan || detail.Identity.Version != domain.PlanVersion(detail.Content) {
		j.t.Fatal("bound identity/source differs", detail)
	}
	j.console.key(j.t, "o")
	windowsOuterReviewSource(j.t, j.ctx, j.console, detail)
	j.console.key(j.t, "y")
	j.visibleJSON("Confirm: Approve all inspected original source bytes", detail.Identity)
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, "Immutable approved snapshot")
	var approved domain.PlanDetail
	j.host.want(j.t, "GET", base+"/plan?workspace_id="+m.workspace.ID, nil, &approved, 200)
	if approved.Approval == nil || approved.Approval.Identity != detail.Identity || !bytes.Equal(approved.Approval.Content, detail.Content) || approved.Approval.ID == "" || approved.Approval.ApprovedAt.IsZero() {
		j.t.Fatal("keyboard approval receipt mismatch", approved)
	}
	j.console.contains(j.t, j.ctx, "Immutable approved snapshot")
	m.approval = *approved.Approval
	j.receipt(m)
	j.original(m)
	return m.approval
}

func (j *winOuterNativeJourney) original(m winOuterNativeMission) {
	j.t.Helper()
	raw, err := os.ReadFile(filepath.Join(m.workspace.Root, winFlowPlanPath))
	winFlowCheck(j.t, err)
	if !bytes.Equal(raw, m.approval.Content) || string(raw) != winFlowPlan {
		j.t.Fatal("original source changed")
	}
	var got domain.PlanApproval
	j.host.want(j.t, "GET", winHTTPBase(m.approval)+"/plan/approvals/"+m.approval.ID+"?workspace_id="+m.workspace.ID, nil, &got, 200)
	if !reflect.DeepEqual(got, m.approval) {
		j.t.Fatal("immutable receipt changed", got)
	}
}

func (j *winOuterNativeJourney) execute(m winOuterNativeMission, resume bool) {
	j.t.Helper()
	j.refresh(m)
	key, label := "e", "Start native execution from this receipt"
	if resume {
		key, label = "g", "Resume the original delivery and opening"
	}
	j.console.key(j.t, key)
	if resume {
		s := j.state(m)
		j.visibleJSON("Confirm: "+label, domain.ResumeRequest{Target: m.approval.Identity, ApprovalID: m.approval.ID, Delivery: s.Delivery, OpenedDigest: s.OpenedDigest})
	} else {
		j.visibleJSON("Confirm: "+label, domain.ExecutionRequest{Target: m.approval.Identity, ApprovalID: m.approval.ID})
	}
	j.console.key(j.t, "\r")
	j.console.contains(j.t, j.ctx, "Last verified effect: started")
}

func (j *winOuterNativeJourney) active(m winOuterNativeMission) domain.ExecutionState {
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

func winOuterNativeStopReference(a domain.PlanApproval, s domain.ExecutionState) domain.ExecutionStopRequest {
	return domain.ExecutionStopRequest{Target: a.Identity, ApprovalID: a.ID, Delivery: s.Delivery, RunID: s.RunID}
}

func (j *winOuterNativeJourney) prepareStop(req domain.ExecutionStopRequest, observe bool) {
	j.t.Helper()
	key, label := "z", "Stop this exact native delivery attempt"
	if observe {
		key, label = "y", "Reobserve Stop using the same exact request"
	}
	ctx, cancel := context.WithTimeout(j.ctx, 10*time.Second)
	defer cancel()
	title := "Confirm: " + label
	j.console.key(j.t, key)
	for {
		j.console.mu.Lock()
		view, readErr := j.console.view, j.console.readErr
		j.console.mu.Unlock()
		if readErr != nil {
			j.t.Fatal(readErr)
		}
		if strings.TrimSpace(strings.Split(strings.TrimSpace(view), "\n")[0]) == title && !strings.Contains(view, "Mission request pending") {
			break
		}
		select {
		case <-j.console.changed:
			// Preparing a confirmation is local and read-only. Enter remains a
			// single, separately reviewed dispatch after the full tuple is read.
			j.console.key(j.t, key)
		case <-j.console.done:
			j.t.Fatal("Console ended before exact Stop confirmation")
		case <-ctx.Done():
			j.t.Fatalf("exact Stop confirmation unavailable: %v\n%s", context.Cause(ctx), view)
		}
	}
	j.visibleJSON("Confirm: "+label, req)
	j.observedStops(j.stopCount)
}

func (j *winOuterNativeJourney) visibleJSON(title string, want any) {
	j.t.Helper()
	outerInspectionReviewJSON(j.t, j.ctx, j.console, title, want)
}

func (j *winOuterNativeJourney) receipt(m winOuterNativeMission) {
	j.t.Helper()
	j.console.key(j.t, "k")
	j.visibleJSON("Immutable approved snapshot", m.approval)
	j.console.key(j.t, "\x1b")
	j.normal()
	j.refresh(m)
}

func (j *winOuterNativeJourney) stopDispatch(req domain.ExecutionStopRequest, count, status int) {
	j.t.Helper()
	snapshot := j.observedStops(count)
	for i, record := range snapshot.Records {
		body := winFlowJSON(j.t, req)
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		if record.Sequence != i+1 || record.Path != winHTTPBase(domain.PlanApproval{Identity: req.Target})+"/execution/stop" || record.Request == nil || *record.Request != req || record.Truncated || record.BodyBytes != len(body) || record.BodySHA256 != digest {
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
	j.t.Logf("SERVER-BOUNDARY HTTP Stop count=%d status=%d request=%s (not raw packet capture)", count, status, winFlowJSON(j.t, req))
}

func (j *winOuterNativeJourney) pendingStop(req domain.ExecutionStopRequest, count int) {
	j.t.Helper()
	j.stopDispatch(req, count, http.StatusAccepted)
	// Full retained 202 observations are paged after unlocking; rendering them
	// must not consume the real Core finalization budget while the journal is held.
}

func (j *winOuterNativeJourney) stopOutcome(req domain.ExecutionStopRequest, effect domain.ExecutionStopEffect, count int) domain.ExecutionStopResult {
	j.t.Helper()
	snapshot := j.observedStops(count)
	status := snapshot.Records[count-1].Status
	if status != 202 && status != 200 && status != 503 {
		j.t.Fatal("unexpected actual Stop status", status)
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
	j.t.Logf("RENDERED STOP response=%s", winFlowJSON(j.t, latest))
	return latest
}

func (j *winOuterNativeJourney) ship(m winOuterNativeMission, run domain.ExecutionState) {
	j.t.Helper()
	// Real source/Core inspection is fixture evidence, separately from the TUI.
	j.host.ship(j.t, j.ctx, m.approval, domain.ExecutionResult{Delivery: run.Delivery, OpenedDigest: run.OpenedDigest})
	j.selectMission(m)
	j.refresh(m)
	j.console.contains(j.t, j.ctx, "Done - current cleared gate and evidence")
	j.receipt(m)
	visible := j.progressFresh(m)
	if visible.Target != m.approval.Identity || visible.ApprovalID != m.approval.ID || visible.Active || visible.RunID != "" || visible.Delivery != run.Delivery || visible.OpenedDigest != run.OpenedDigest || visible.Watch == nil || visible.Watch.Source != domain.SourceCurrent || visible.Watch.Header["state"] != loop.StateDone || visible.Gate == nil || !visible.Gate.Required || !visible.Gate.Cleared || visible.Gate.EvidenceDigest == "" {
		j.t.Fatal("rendered progress is not actual SHIP", visible)
	}
}

type winOuterNativeStopView struct {
	Latest         bool                         `json:"latest_observation"`
	MatchesCurrent bool                         `json:"matches_current_attempt"`
	Request        *domain.ExecutionStopRequest `json:"request"`
	Result         *domain.ExecutionStopResult  `json:"result"`
	Effect         string                       `json:"transport_effect"`
	Error          string                       `json:"error"`
}

type winOuterNativeProgress struct {
	Mission   string                       `json:"mission"`
	Workspace string                       `json:"workspace"`
	Execution *domain.ExecutionState       `json:"execution"`
	Watch     domain.WatchView             `json:"watch"`
	Fresh     bool                         `json:"fresh"`
	Uncertain string                       `json:"unresolved_mutation"`
	Problem   string                       `json:"problem"`
	Error     string                       `json:"problem_error"`
	Pending   *domain.ExecutionStopRequest `json:"pending_stop"`
	Stops     []winOuterNativeStopView     `json:"stops"`
}

func (j *winOuterNativeJourney) inspection() winOuterNativeProgress {
	j.t.Helper()
	j.console.key(j.t, "w")
	raw, _ := outerInspectionRead(j.t, j.ctx, j.console, "Progress snapshot (Esc, f, w refresh) / retained Stop observations", "")
	var visible winOuterNativeProgress
	winFlowCheck(j.t, winFlowDecode(raw, &visible))
	j.snapshotNumber++
	winFlowWrite(j.t, filepath.Join(j.cfg.Root, fmt.Sprintf("progress-%d.json", j.snapshotNumber)), raw)
	j.console.key(j.t, "\x1b")
	j.normal()
	return visible
}

func (j *winOuterNativeJourney) normal() {
	j.t.Helper()
	// The normal mission header can scroll above an 80x24 screen. Identity is
	// checked in the complete paged document, before this mode-exit observation.
	j.console.screen(j.t, j.ctx, "normal mission controls after inspection", func(s string) bool {
		return strings.Contains(s, "[ / ] select  v view/reconnect") && !strings.Contains(s, "Progress snapshot (Esc, f, w refresh)") && !strings.Contains(s, "Confirm:")
	})
}

func (j *winOuterNativeJourney) progressFresh(m winOuterNativeMission) domain.ExecutionState {
	j.t.Helper()
	visible := j.inspection()
	actual := j.state(m)
	if !winOuterNativeProgressMatches(visible, m, actual) {
		j.t.Fatal("current progress identity/watch/freshness differs from independent public state", visible, actual)
	}
	return *visible.Execution
}

func winOuterNativeProgressMatches(visible winOuterNativeProgress, m winOuterNativeMission, actual domain.ExecutionState) bool {
	if visible.Execution == nil || visible.Mission != m.mission.ID || visible.Workspace != m.workspace.ID || visible.Execution.Target != m.approval.Identity || visible.Execution.ApprovalID != m.approval.ID || visible.Mission != visible.Execution.Target.MissionID || visible.Workspace != visible.Execution.Target.Binding.WorkspaceID || (visible.Execution.Watch != nil && !reflect.DeepEqual(visible.Watch, *visible.Execution.Watch)) || (visible.Execution.Watch == nil && !reflect.DeepEqual(visible.Watch, domain.WatchView{})) || !visible.Fresh || visible.Uncertain != "" || visible.Problem != "" || visible.Error != "" || visible.Pending != nil {
		return false
	}
	return winOuterNativeExecutionEqual(*visible.Execution, actual)
}

func winOuterNativeExecutionEqual(visible, actual domain.ExecutionState) bool {
	// Consecutive public reads compute age at different instants. Preserve all
	// raw evidence and compare every field except this validated duration.
	withoutAge := func(state domain.ExecutionState) (domain.ExecutionState, bool) {
		if state.Watch == nil {
			return state, !state.Bound && state.OpenedDigest == ""
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

func winOuterNativeStart(t *testing.T, ctx context.Context, f *windowsOuterHarness, workspace string) *windowsOuterConsole {
	t.Helper()
	id := fmt.Sprintf("outer-console-%d", len(f.consoles)+1)
	logs := filepath.Join(f.root, id)
	windowsOuterDirectory(t, logs)
	p := &windowsOuterConsole{manager: session.New(logs), ctx: ctx, done: make(chan struct{}), changed: make(chan struct{}, 1)}
	f.consoles = append(f.consoles, p)
	t.Cleanup(func() { winOuterNativeCleanup(t, p, f.root) })
	argv := []string{f.binary, "-home", f.home, "-listen", f.address}
	pending, err := p.manager.StartPending(domain.Session{ID: id, Argv: argv, Rows: 24, Cols: 80, Owner: "tui", OwnerClient: "outer-qualification"}, f.env, workspace, 0)
	if err != nil {
		close(p.done)
		t.Fatalf("actual ordinary ConPTY startup: %v", err)
	}
	p.pending = pending
	p.sub, err = p.manager.SubscribeTerminal(id)
	if err != nil {
		close(p.done)
		t.Fatal(err)
	}
	snap := pending.Snapshot()
	t.Logf("PROCESS actual ConPTY session=%s pid=%d incarnation=%s argv=%q size=80x24", id, snap.PID, snap.ProcessIdentity, argv)
	go winOuterNativeRead(p)
	// This query-only witness is bound to the Manager's observed incarnation;
	// all termination remains with the Manager's original native Job.
	p.witness, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(snap.PID))
	if err != nil {
		t.Fatal(err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.witness, &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	identity := fmt.Sprintf("windows:%d:%d", snap.PID, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	if identity != snap.ProcessIdentity {
		t.Fatalf("process incarnation changed: observed=%s wanted=%s", identity, snap.ProcessIdentity)
	}
	if snap.PID <= 0 || snap.ProcessIdentity == "" {
		t.Fatal("native process birth observation missing")
	}
	p.contains(t, ctx, "w workspaces (select / create / setup)")
	return p
}

func winOuterNativeRead(p *windowsOuterConsole) {
	defer close(p.done)
	screen := terminalengine.NewScreen(24, 80)
	screen.SetScrollbackLimit(0)
	parser := terminalengine.NewParser(screen)
	for {
		event, err := p.sub.Next(context.Background())
		p.mu.Lock()
		if event.Cols != 0 && (event.Cols != 80 || event.Rows != 24) {
			p.readErr = fmt.Errorf("unexpected native geometry: %dx%d", event.Cols, event.Rows)
		}
		if len(event.Data) > 0 {
			if p.raw.Len()+len(event.Data) <= 4<<20 {
				p.raw.Write(event.Data)
			} else {
				p.readErr = errors.New("outer Console exceeded 4 MiB output bound")
			}
			parser.Feed(event.Data)
			p.view = ansi.Strip(screen.Render())
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				p.eof = true
			} else {
				p.readErr = errors.Join(p.readErr, err)
			}
		}
		p.mu.Unlock()
		select {
		case p.changed <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

func newWinOuterNativeJourney(t *testing.T, provider string) *winOuterNativeJourney {
	t.Helper()
	// Supplied by Root; never infer a module cache from the developer's HOME.
	cache := os.Getenv("GOMODCACHE")
	if !filepath.IsAbs(cache) {
		t.Fatal("explicit absolute offline GOMODCACHE required")
	}
	if os.Getenv("BATUTA_TEST_WINDOWS_PUBLIC_FLOW_SOURCE_MANIFEST") == "" || os.Getenv("BATUTA_TEST_WINDOWS_PUBLIC_FLOW_SOURCE_REVISION") == "" {
		t.Fatal("externally supplied full source manifest and revision required")
	}
	cfg := winFlowFixture(t, provider, true)
	cfg.ObserveStops = true
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	t.Cleanup(cancel)
	binary := windowsOuterBuild(t, ctx, filepath.Join(cfg.Root, "consumer"), cache, runtime.GOROOT(), winFlowEnvironment(cfg))
	winFlowVerifyBinary(t, binary, winFlowHash(t, binary), "github.com/batuta-ai/console/cmd/console", false)
	winFlowVerifySource(t, cfg)
	j := &winOuterNativeJourney{t: t, ctx: ctx, cfg: cfg}
	j.harness = &windowsOuterHarness{root: cfg.Root, home: cfg.Home, binary: binary, env: append(winFlowEnvironment(cfg), "TERM=xterm-256color", "COLORTERM=truecolor")}
	winFlowWrite(t, filepath.Join(cfg.Root, "consumer-qualification.json"), winFlowJSON(t, map[string]any{
		"console_sha256": winFlowHash(t, binary), "console_cgo": false, "console_race": false, "host_core_race": cfg.Race,
		"go": runtime.Version(), "core": cfg.CoreVersion, "source_revision": cfg.SourceRevision, "source_digest": cfg.SourceDigest,
		"qualification_sha256": winFlowHash(t, "outer_native_stop_journey_windows_test.go"), "observer_sha256": winFlowHash(t, "public_flow_stop_observer_windows_test.go"),
		"geometry": "80x24", "claims": "synthetic providers; actual Core/Service/Store/Manager; no accounts or packaged daemon",
	}))
	j.launchHost()
	j.account = winHTTPAccount(t, j.host)
	return j
}

func (j *winOuterNativeJourney) launchHost() {
	j.host = &winFlowHTTP{winFlowLaunch(j.t, j.cfg)}
	j.harness.address = j.host.address
	j.stopCount = 0
	j.observedStops(0)
}

func (j *winOuterNativeJourney) launchConsole() {
	j.console = winOuterNativeStart(j.t, j.ctx, j.harness, j.cfg.Root)
}

func winOuterNativeCleanup(t *testing.T, p *windowsOuterConsole, root string) {
	t.Helper()
	// The real host owns a separate drain. The legacy harness finish method
	// closes its fake API/Store and is not authority for this host's lifetime.
	defer func() {
		p.mu.Lock()
		raw, readErr := append([]byte(nil), p.raw.Bytes()...), p.readErr
		p.mu.Unlock()
		name := "unstarted"
		if p.pending != nil {
			name = p.pending.Snapshot().ID
		}
		winFlowWrite(t, filepath.Join(root, name+".terminal.raw"), raw)
		if readErr != nil {
			t.Error(readErr)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.manager.Shutdown(ctx); err != nil {
		t.Errorf("outer join uncertain; retaining all dependencies: %v", err)
		return
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		t.Error("outer reader unjoined; retaining dependencies")
		return
	}
	if p.sub != nil {
		p.sub.Close()
	}
	if p.witness != 0 {
		winFlowCheck(t, windows.CloseHandle(p.witness))
		p.witness = 0
	}
}

func (j *winOuterNativeJourney) closeConsole() {
	j.console.quit(j.t, j.ctx)
	p := j.console
	p.mu.Lock()
	raw := append([]byte(nil), p.raw.Bytes()...)
	p.mu.Unlock()
	winFlowWrite(j.t, filepath.Join(j.cfg.Root, p.pending.Snapshot().ID+".terminal.raw"), raw)
}

func (j *winOuterNativeJourney) observedStops(count int) winFlowStopSnapshot {
	j.t.Helper()
	var snapshot winFlowStopSnapshot
	winFlowCheck(j.t, winHTTPAwaitResult(j.ctx, "server-boundary Stop completion", func() (bool, error) {
		event := j.host.command(j.t, j.ctx, winFlowCommand{Op: "stop_observations"})
		if event.Stops == nil {
			return false, errors.New("missing Stop observer snapshot")
		}
		snapshot = *event.Stops
		if !snapshot.Enabled || snapshot.Overflow || snapshot.Count > count || len(snapshot.Records) > count {
			return false, fmt.Errorf("unexpected Stop dispatch: %+v want=%d", snapshot, count)
		}
		return snapshot.Count == count && snapshot.InFlight == 0 && len(snapshot.Records) == count, nil
	}))
	j.stopCount = count
	return snapshot
}

func (j *winOuterNativeJourney) state(m winOuterNativeMission) domain.ExecutionState {
	j.t.Helper()
	var state domain.ExecutionState
	j.host.awaitReadiness(j.t, j.ctx, m.approval, "exact typed read-only readiness", func(s domain.ExecutionState) bool { state = s; return true })
	return state
}

func (j *winOuterNativeJourney) refresh(m winOuterNativeMission) {
	j.t.Helper()
	// The caller has already joined its last mutation. Readiness permits only the
	// existing typed plan_io read transition, under this same inherited context.
	_ = j.state(m)
	// w is an immutable local snapshot, never an acknowledgement of an f request.
	// The screen helper also refuses a displayed pending mission request.
	before := j.inspection()
	if before.Mission != m.mission.ID || before.Workspace != m.workspace.ID || before.Pending != nil || before.Uncertain != "" {
		j.t.Fatal("unsafe snapshot/refresh boundary", before)
	}
	j.refreshAfterEscape(m)
}

func (j *winOuterNativeJourney) refreshAfterEscape(m winOuterNativeMission) {
	j.t.Helper()
	// Observe Esc's mode exit before another key; adjacent ESC+f can be read as
	// Alt-f. The exit observation grants neither freshness nor mutation authority.
	j.normal()
	j.refreshCurrent(m)
	// At 80x24 the normal header can be clipped. Page every snapshot byte and
	// verify the exact receipt/target and all current facts independently.
	j.progressFresh(m)
	// Inspection's Esc invalidates freshness again. Restore the normal view for
	// the next exact confirmation; neither this frame nor w is a per-f ACK.
	j.refreshCurrent(m)
}

func winOuterNativeRefreshFrame(s string) bool {
	return !strings.Contains(s, "Mission request pending") && !strings.Contains(s, "Confirm:") && (strings.Contains(s, "Request failed") || strings.Contains(s, "Last verified effect: read_only") && !strings.Contains(s, "refresh required"))
}

func (j *winOuterNativeJourney) refreshCurrent(m winOuterNativeMission) {
	j.t.Helper()
	// Esc's stale frame may be coalesced with a periodic read. Only the latest
	// validated facts matter; no raw-output length or transient-frame barrier.
	for {
		_ = j.state(m)
		j.console.key(j.t, "f")
		screen := j.console.screen(j.t, j.ctx, "current read-only facts (explicit or periodic)", winOuterNativeRefreshFrame)
		if !strings.Contains(screen, "Request failed") {
			return
		}
		if !strings.Contains(screen, "plan_io") {
			j.t.Fatal("non-readiness refresh failure", screen)
		}
		// No paid request is retried; the typed readiness helper checks exact API
		// error identity and context before and after each read-only observation.
		_ = j.state(m)
	}
}

// Qualify the consumer's current-facts predicate through the actual read-only
// model/client/API/Service/Store path. No provider, admission or terminal record.
func TestWindowsOuterNativeRefreshCurrentFacts(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, refresh := range []string{"periodic-latest-frame", "explicit-f"} {
		t.Run(refresh, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				check := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				root := t.TempDir()
				path := ".batuta/plans/reading.md"
				check(os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700))
				check(os.WriteFile(filepath.Join(root, path), []byte("# Plan — Reading\n**Status:** approved\n- [ ] 1. Read evidence — data/high\n  Accept: all bytes\n"), 0600))
				st, err := store.Open(filepath.Join(t.TempDir(), "reading.db"))
				check(err)
				t.Cleanup(func() { check(st.Close()) })
				workspace := domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}
				check(st.PutWorkspace(workspace))
				check(st.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}))
				c := client.New("http://inspection.invalid", "inspection")
				c.HTTP.Transport = inspectionAPITransport{(&api.Server{Svc: service.New(st, nil, nil), Token: "inspection"}).Handler()}
				mission, err := c.BindMission(t.Context(), "m", client.MissionBindingRequest{WorkspaceID: "w", PlanPath: path})
				check(err)
				detail, err := c.PlanDetail(t.Context(), "m", "w")
				check(err)
				approved, err := c.ApprovePlan(t.Context(), detail.Identity)
				check(err)
				if approved.Receipt == nil {
					t.Fatal("missing public approval receipt")
				}
				want := winOuterNativeMission{workspace: workspace, mission: mission, approval: *approved.Receipt}
				m := inspectionSize(t, model{api: c}.selectMission(mission), 80, 24)
				m = journeyKey(t, m, "f")
				m = journeyKey(t, m, "w")
				m = journeyKey(t, m, "esc")
				if winOuterNativeRefreshFrame(m.View().Content) {
					t.Fatal("stale Esc frame accepted")
				}
				if refresh == "periodic-latest-frame" {
					m = inspectionBackgroundTick(t, m)
				} else {
					m = journeyKey(t, m, "f")
				}
				frame := m.View().Content
				if !winOuterNativeRefreshFrame(frame) || strings.Contains(frame, "refresh required") {
					t.Fatal("current latest frame rejected after Esc/read", frame)
				}
				for _, busy := range []string{"Mission request pending", "Confirm: Stop", "refresh required"} {
					if winOuterNativeRefreshFrame(frame + "\n" + busy) {
						t.Fatal("unsafe normal frame accepted", busy)
					}
				}
				m = journeyKey(t, m, "w")
				_, raw := inspectionDocument(t, m)
				var visible winOuterNativeProgress
				check(json.Unmarshal(raw, &visible))
				actual, err := c.ExecutionState(t.Context(), "m", "w", want.approval.ID)
				check(err)
				if actual.Active || actual.Bound || actual.OpenedDigest != "" || actual.Watch != nil || !winOuterNativeProgressMatches(visible, want, actual) {
					t.Fatal("current paged facts do not match independent unstarted public state", visible, actual)
				}
				for name, change := range map[string]func(*winOuterNativeProgress){
					"stale":       func(v *winOuterNativeProgress) { v.Fresh = false },
					"mission":     func(v *winOuterNativeProgress) { v.Mission = "other" },
					"workspace":   func(v *winOuterNativeProgress) { v.Workspace = "other" },
					"receipt":     func(v *winOuterNativeProgress) { v.Execution.ApprovalID = "other" },
					"target":      func(v *winOuterNativeProgress) { v.Execution.Target.Version = "other" },
					"delivery":    func(v *winOuterNativeProgress) { v.Execution.Delivery = "other" },
					"run":         func(v *winOuterNativeProgress) { v.Execution.RunID = "other" },
					"uncertain":   func(v *winOuterNativeProgress) { v.Uncertain = "answer" },
					"problem":     func(v *winOuterNativeProgress) { v.Problem = "journey_failed" },
					"error":       func(v *winOuterNativeProgress) { v.Error = "plan_io" },
					"pending":     func(v *winOuterNativeProgress) { v.Pending = &domain.ExecutionStopRequest{} },
					"missing":     func(v *winOuterNativeProgress) { v.Execution = nil },
					"watch-drift": func(v *winOuterNativeProgress) { v.Watch.MissionID = "other" },
				} {
					var changed winOuterNativeProgress
					check(json.Unmarshal(raw, &changed))
					change(&changed)
					if winOuterNativeProgressMatches(changed, want, actual) {
						t.Fatal("unsafe snapshot accepted", name)
					}
				}
				other := want
				other.approval.ID = "other"
				if winOuterNativeProgressMatches(visible, other, actual) {
					t.Fatal("self-consistent snapshot for the wrong requested receipt accepted")
				}
				t.Log("actual public unstarted state; latest frame may come from a periodic read; full 80x24 snapshot bytes validated; no per-f ACK or native execution claim")
			})
		})
	}
}

func (j *winOuterNativeJourney) rejectOld(req domain.ExecutionStopRequest) {
	j.t.Helper()
	before := j.stopCount
	j.host.rejectStop(j.t, j.ctx, req)
	snapshot := j.observedStops(before + 1)
	record := snapshot.Records[before]
	if record.Request == nil || *record.Request != req || record.Status != 400 {
		j.t.Fatal("disclosed stale-nonce negative differs at boundary", record)
	}
	j.t.Logf("DISCLOSED public negative stale Stop status=400 plan_reference request=%s", winFlowJSON(j.t, req))
}

func winOuterNativeResult(s domain.ExecutionState) domain.ExecutionResult {
	return domain.ExecutionResult{Target: s.Target, ApprovalID: s.ApprovalID, Delivery: s.Delivery, OpenedDigest: s.OpenedDigest, RunID: s.RunID}
}

func TestWindowsOuterConsoleConPTYNativeStopRestartSHIP(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			j := newWinOuterNativeJourney(t, provider)
			a, b := j.catalog(true), j.catalog(false)
			j.launchConsole()
			a.approval = j.approve(a)
			b.approval = j.approve(b)
			if a.approval.ID == b.approval.ID || a.mission.ID == b.mission.ID || a.workspace.ID == b.workspace.ID || strings.EqualFold(a.workspace.Root, b.workspace.Root) {
				t.Fatal("missions/receipts/canonical roots not independent")
			}
			j.host.catalog(t, j.account, a.approval, b.approval)
			j.selectMission(a)
			j.execute(a, false)
			var first domain.ExecutionState
			j.host.awaitReadiness(t, j.ctx, a.approval, "A parked actual question", func(s domain.ExecutionState) bool {
				if s.Active || s.Watch == nil || s.Watch.PendingQuestion == nil {
					return false
				}
				first = s
				return true
			})
			question := *first.Watch.PendingQuestion
			q := question.Target
			if q.MissionID != a.mission.ID || q.WorkspaceID != a.workspace.ID || q.Delivery != first.Delivery || q.OpenedDigest != first.OpenedDigest || q.Task == "" || q.Execution < 1 || q.QuestionID == "" || question.Prompt != "exact native question?" {
				t.Fatal("incomplete actual question identity", question)
			}
			j.refresh(a)
			visible := j.progressFresh(a)
			if visible.Watch == nil || visible.Watch.PendingQuestion == nil || !reflect.DeepEqual(*visible.Watch.PendingQuestion, question) {
				t.Fatal("visible full question differs", visible)
			}
			j.refreshAfterEscape(a)
			before := winHTTPJournal(t, j.ctx, a.approval, first.Delivery)
			j.console.key(t, "j")
			j.console.contains(t, j.ctx, strconv.Quote(question.Prompt))
			j.console.key(t, "exact native\ranswer\t\r")
			j.visibleJSON("Confirm: Record this answer; resume remains separate", q.Answer("exact native\nanswer"))
			j.console.key(t, "\r")
			j.console.contains(t, j.ctx, "Last verified effect: recorded")
			answered := j.state(a)
			after := winHTTPJournal(t, j.ctx, a.approval, first.Delivery)
			if answered.Active || answered.RunID != "" || answered.Watch == nil || answered.Watch.PendingQuestion != nil || len(after) != len(before)+1 || !reflect.DeepEqual(before, after[:len(before)]) || after[len(after)-1].Kind != loop.KindAnswer {
				t.Fatal("answer replayed/auto-admitted or rewrote opening", answered)
			}
			var answer struct {
				Execution int    `json:"execution"`
				Answer    string `json:"answer"`
				Question  string `json:"question"`
			}
			winFlowCheck(t, winFlowDecode(after[len(after)-1].Detail, &answer))
			if after[len(after)-1].TaskID != q.Task || answer.Execution != q.Execution || answer.Answer != "exact native\nanswer" || answer.Question != question.Prompt {
				t.Fatal("persisted exact answer differs", answer)
			}
			if _, err := os.Stat(filepath.Join(a.workspace.Root, ".git", "public-flow", "first.birth.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("answer launched provider", err)
			}
			j.execute(a, true)
			aRun := j.active(a)
			if aRun.Delivery != first.Delivery || aRun.OpenedDigest != first.OpenedDigest {
				t.Fatal("explicit Resume changed opening")
			}
			target := j.host.tree(t, j.ctx, a.approval, "first")
			j.selectMission(b)
			j.execute(b, false)
			bRun := j.active(b)
			sibling := j.host.tree(t, j.ctx, b.approval, "first")
			if aRun.RunID == bRun.RunID || aRun.Delivery == bRun.Delivery || target.birth.Root == sibling.birth.Root || target.birth.Child == sibling.birth.Child || *target.birth.Core == *sibling.birth.Core {
				t.Fatal("capacity-two jobs not independent")
			}
			for _, m := range []winOuterNativeMission{a, b} {
				s := j.state(m)
				if !s.Active || !domain.ValidExecutionRunID(s.RunID) {
					t.Fatal("actual capacity two not occupied", s)
				}
			}
			target.alive(t)
			sibling.alive(t)
			foreign := j.host.command(t, j.ctx, winFlowCommand{Op: "foreign"})
			if foreign.Tree == nil || foreign.Tree.Root.PID == 0 || foreign.Tree.Root.Created == 0 {
				t.Fatal("missing foreign birth")
			}
			j.selectMission(a)
			j.refresh(a)
			req := winOuterNativeStopReference(a.approval, aRun)
			j.prepareStop(req, false)
			j.console.key(t, "\x1b")
			j.refreshAfterEscape(a)
			j.observedStops(0)
			j.selectMission(b)
			j.refresh(b)
			j.prepareStop(winOuterNativeStopReference(b.approval, bRun), false)
			j.console.key(t, "\x1b")
			j.refreshAfterEscape(b)
			j.observedStops(0)
			j.selectMission(a)
			j.refresh(a)
			j.prepareStop(req, false)
			lock := winFlowCommand{Op: "hold", Root: a.workspace.Root, Delivery: aRun.Delivery, Phase: "first"}
			if !j.host.command(t, j.ctx, lock).Blocked {
				t.Fatal("actual journal lock missing")
			}
			originalCtx := j.ctx
			stopCtx, stopCancel := context.WithTimeout(originalCtx, 5*time.Second)
			defer stopCancel()
			j.ctx = stopCtx
			j.console.ctx = stopCtx
			j.console.key(t, "\r")
			j.pendingStop(req, 1)
			target.retired(t, stopCtx, false)
			j.host.observe(t, stopCtx, "retired", a.approval, "first")
			j.host.observe(t, stopCtx, "core-alive", a.approval, "first")
			sibling.alive(t)
			j.host.command(t, stopCtx, winFlowCommand{Op: "foreign-alive"})
			j.prepareStop(req, true)
			j.console.key(t, "\r")
			j.pendingStop(req, 2)
			lock.Op = "unlock"
			j.host.command(t, stopCtx, lock)
			j.host.awaitReadiness(t, stopCtx, a.approval, "inactive durable canceled under original Stop budget", func(s domain.ExecutionState) bool {
				return !s.Active && s.RunID == "" && s.Watch != nil && s.Watch.Header["state"] == loop.StateCanceled
			})
			target.retired(t, stopCtx, true)
			j.host.observe(t, stopCtx, "core-retired", a.approval, "first")
			winFlowCheck(t, context.Cause(stopCtx))
			j.ctx = originalCtx
			j.console.ctx = originalCtx
			stopCancel()
			// Full retained pages are read after unlocking, without spending Core's
			// existing finalization budget on terminal rendering.
			pending := j.stopOutcome(req, domain.ExecutionStopPending, 2)
			winHTTPPending(t, pending, 202)
			j.refresh(a)
			j.prepareStop(req, true)
			j.console.key(t, "\r")
			completed := j.stopOutcome(req, domain.ExecutionStopCompleted, 3)
			if !completed.Finalized || !completed.Durable || completed.CoreState != loop.StateCanceled || completed.State == nil || completed.State.Active || completed.State.RunID != "" || completed.State.Target != a.approval.Identity || completed.State.ApprovalID != a.approval.ID || completed.State.Delivery != first.Delivery || completed.State.OpenedDigest != first.OpenedDigest || completed.State.Watch == nil || completed.State.Watch.Header["state"] != loop.StateCanceled || !strings.Contains(strings.Join(completed.Causes, "\n"), context.Canceled.Error()) {
				t.Fatal("Stop lacks actual durable canceled identity", completed)
			}
			j.refresh(a)
			j.console.contains(t, j.ctx, "Completion is not verified")
			sibling.alive(t)
			j.host.command(t, j.ctx, winFlowCommand{Op: "foreign-alive"})
			j.host.observe(t, j.ctx, "release", b.approval, "first")
			j.ship(b, bRun)
			sibling.retired(t, j.ctx, true)
			j.host.observe(t, j.ctx, "core-retired", b.approval, "first")
			aRecords, bRecords := winHTTPJournal(t, j.ctx, a.approval, aRun.Delivery), winHTTPJournal(t, j.ctx, b.approval, bRun.Delivery)
			winHTTPOpening(t, aRecords, a.approval, winOuterNativeResult(first))
			j.original(a)
			j.original(b)
			oldHost := j.host
			j.closeConsole()
			j.host.drain(t)
			j.launchHost()
			// winFlowLaunch validates this new command's ready PID/config. A port
			// (or PID) may legally be reused after the old process fully joins.
			if !oldHost.closed || !oldHost.joined || oldHost.cmd.ProcessState == nil || !oldHost.cmd.ProcessState.Success() || j.host.winFlowHost == oldHost.winFlowHost || j.host.cmd == oldHost.cmd || j.host.token != oldHost.token {
				t.Fatal("restart host join/ready/auth identity drift")
			}
			t.Logf("RESTART old host pid=%d joined=%v new ready host pid=%d address=%s", oldHost.cmd.Process.Pid, oldHost.joined, j.host.cmd.Process.Pid, j.host.address)
			j.launchConsole()
			j.selectMission(a)
			j.refresh(a)
			j.receipt(a)
			recovered := j.progressFresh(a)
			if recovered.Active || recovered.RunID != "" || recovered.Target != a.approval.Identity || recovered.ApprovalID != a.approval.ID || recovered.Delivery != first.Delivery || recovered.OpenedDigest != first.OpenedDigest || recovered.Watch == nil || recovered.Watch.Header["state"] != loop.StateCanceled {
				t.Fatal("restart lost original canceled receipt/delivery", recovered)
			}
			j.ship(b, bRun)
			if !reflect.DeepEqual(aRecords, winHTTPJournal(t, j.ctx, a.approval, aRun.Delivery)) || !reflect.DeepEqual(bRecords, winHTTPJournal(t, j.ctx, b.approval, bRun.Delivery)) {
				t.Fatal("restart/navigation replayed actual journals")
			}
			j.original(a)
			j.original(b)
			j.rejectOld(req)
			j.host.observe(t, j.ctx, "phase", a.approval, "replacement")
			j.selectMission(a)
			j.execute(a, true)
			next := j.active(a)
			if next.RunID == aRun.RunID || next.RunID == bRun.RunID || next.Delivery != first.Delivery || next.OpenedDigest != first.OpenedDigest {
				t.Fatal("replacement reused nonce or changed opening", next)
			}
			replacement := j.host.tree(t, j.ctx, a.approval, "replacement")
			if replacement.birth.Root == target.birth.Root || replacement.birth.Child == target.birth.Child || *replacement.birth.Core == *target.birth.Core {
				t.Fatal("replacement reused native birth")
			}
			j.rejectOld(req)
			still := j.state(a)
			if !still.Active || still.RunID != next.RunID {
				t.Fatal("stale Stop controlled replacement", still)
			}
			replacement.alive(t)
			j.host.observe(t, j.ctx, "release", a.approval, "replacement")
			j.ship(a, next)
			replacement.retired(t, j.ctx, true)
			j.host.observe(t, j.ctx, "core-retired", a.approval, "replacement")
			if !reflect.DeepEqual(bRecords, winHTTPJournal(t, j.ctx, b.approval, bRun.Delivery)) {
				t.Fatal("A replacement changed B journal")
			}
			j.original(a)
			j.original(b)
			j.closeConsole()
			j.host.drain(t)
		})
	}
}

func TestWindowsOuterConsoleConPTYNativeStopJournalFault(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			j := newWinOuterNativeJourney(t, provider)
			a := j.catalog(false)
			j.launchConsole()
			a.approval = j.approve(a)
			j.execute(a, false)
			run := j.active(a)
			tree := j.host.tree(t, j.ctx, a.approval, "first")
			foreign := j.host.command(t, j.ctx, winFlowCommand{Op: "foreign"})
			if foreign.Tree == nil || foreign.Tree.Root.PID == 0 {
				t.Fatal("missing independent foreign witness")
			}
			j.refresh(a)
			req := winOuterNativeStopReference(a.approval, run)
			j.prepareStop(req, false)
			lock := winFlowCommand{Op: "hold", Root: a.workspace.Root, Delivery: run.Delivery, Phase: "first"}
			if !j.host.command(t, j.ctx, lock).Blocked {
				t.Fatal("actual journal lock missing")
			}
			originalCtx := j.ctx
			stopCtx, stopCancel := context.WithTimeout(originalCtx, 5*time.Second)
			defer stopCancel()
			j.ctx = stopCtx
			j.console.ctx = stopCtx
			j.console.key(t, "\r")
			j.pendingStop(req, 1)
			tree.retired(t, stopCtx, false)
			j.host.observe(t, stopCtx, "retired", a.approval, "first")
			j.host.observe(t, stopCtx, "core-alive", a.approval, "first")
			lock.Op = "corrupt"
			if !j.host.command(t, stopCtx, lock).SameFile {
				t.Fatal("invalid append changed original fileID")
			}
			tree.retired(t, stopCtx, true)
			j.host.observe(t, stopCtx, "core-retired", a.approval, "first")
			winFlowCheck(t, context.Cause(stopCtx))
			// Each reobservation is a reviewed keyboard confirmation, never an HTTP
			// positive substitute or a retry of a paid mutation after a timeout.
			count := 1
			var final domain.ExecutionStopResult
			for {
				j.prepareStop(req, true)
				j.console.key(t, "\r")
				count++
				snapshot := j.observedStops(count)
				status := snapshot.Records[count-1].Status
				if status == 202 {
					j.pendingStop(req, count)
					continue
				}
				if status != 503 {
					t.Fatal("fault status", status)
				}
				winFlowCheck(t, context.Cause(stopCtx))
				j.ctx = originalCtx
				j.console.ctx = originalCtx
				stopCancel()
				// The original read fails on the corrupt journal. Its retained snapshot
				// must not promote the earlier running view to fresh durable authority.
				j.console.key(t, "f")
				final = j.stopOutcome(req, domain.ExecutionStopUncertain, count)
				break
			}
			causes := strings.Join(final.Causes, "\n")
			if !final.Finalized || final.Durable || final.State != nil || (final.CoreState != "" && final.CoreState != loop.StateCanceled) || final.Error == nil || final.Error.Code != "execution_stop_uncertain" || final.Error.Message != "native finalization requires inspection" || !strings.Contains(causes, context.Canceled.Error()) || !strings.Contains(causes, batuta.ErrArtifactInvalid.Error()) {
				t.Fatal("fault concealed independent actual causes or invented durability", final)
			}
			j.selectMission(a)
			retained := j.stopOutcome(req, domain.ExecutionStopUncertain, count)
			if !reflect.DeepEqual(final, retained) {
				t.Fatal("navigation lost exact uncertainty", final, retained)
			}
			j.console.key(t, "f")
			retained = j.stopOutcome(req, domain.ExecutionStopUncertain, count)
			if !reflect.DeepEqual(final, retained) {
				t.Fatal("read-only failure changed retained uncertainty")
			}
			j.host.command(t, j.ctx, winFlowCommand{Op: "foreign-alive"})
			j.original(a)
			j.closeConsole()
			j.host.drain(t)
		})
	}
}
