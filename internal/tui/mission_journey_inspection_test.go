package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func inspectionSize(t *testing.T, m model, width, height int) model {
	t.Helper()
	next, cmd := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if cmd != nil {
		t.Fatal("inspection resize dispatched a command")
	}
	return next.(model)
}
func inspectionKey(t *testing.T, m model, key string) model {
	t.Helper()
	code := map[string]rune{"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "down": tea.KeyDown, "up": tea.KeyUp}[key]
	next, cmd := m.Update(tea.KeyPressMsg{Code: code})
	if cmd != nil {
		t.Fatal("inspection paging dispatched a command")
	}
	return next.(model)
}

// Only cells that fit the terminal are eligible for reconstruction. An overlong
// data row fails before its hidden suffix can become evidence.
func inspectionCells(t *testing.T, m model) []string {
	t.Helper()
	rows := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
	for _, row := range rows {
		if strings.HasPrefix(row, `"{`) && len(row) > m.width {
			t.Fatalf("quoted JSON physically clipped at column %d: %q", m.width, row)
		}
	}
	if len(rows) > m.height {
		t.Fatalf("physical frame has %d rows, height %d", len(rows), m.height)
	}
	for _, row := range rows {
		if len([]rune(row)) > m.width {
			t.Fatalf("physical row exceeds %d columns: %q", m.width, row)
		}
		if strings.ContainsAny(row, "\x1b\r\x00") {
			t.Fatalf("terminal control in rendered cells: %q", row)
		}
	}
	return rows
}
func inspectionDocument(t *testing.T, m model) (model, []byte) {
	t.Helper()
	var raw []byte
	for n := 0; n < 10000; n++ {
		rows := inspectionCells(t, m)
		end := false
		for _, row := range rows {
			if row == "END OF SOURCE" {
				end = true
			}
			if len(row) < 11 || row[8] != ' ' {
				continue
			}
			offset, err := strconv.ParseUint(row[:8], 16, 32)
			if err != nil {
				continue
			}
			part, err := strconv.Unquote(row[9:])
			if err != nil {
				t.Fatal(err, row)
			}
			for _, b := range []byte(row) {
				if b < 32 || b > 126 {
					t.Fatal("data is not terminal-safe ASCII", row)
				}
			}
			if int(offset) != len(raw) {
				t.Fatalf("lost or duplicate bytes: offset %d, reconstructed %d", offset, len(raw))
			}
			raw = append(raw, part...)
		}
		if end {
			if len(raw) == 0 {
				t.Fatal("no visible byte rows")
			}
			return m, raw
		}
		m = inspectionKey(t, m, "pgdown")
	}
	t.Fatal("paging never reached end")
	return m, nil
}
func inspectionFixture() *journeyWire {
	f := newJourneyWire("codex")
	f.detail.Identity.Binding.WorkspaceRoot = "/root/" + strings.Repeat("long path segment/", 35) + "tail"
	f.detail.Identity.Binding.CanonicalRoot = f.detail.Identity.Binding.WorkspaceRoot
	f.mission.Binding = &f.detail.Identity.Binding
	f.receipt.Identity = f.detail.Identity
	f.detail.Approval = &f.receipt
	f.state.Target = f.detail.Identity
	f.state.RunID = strings.Repeat("a", 64)
	f.state.Active = true
	return f
}
func TestInspectionConfirmationPhysicalBytes(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, size := range [][2]int{{240, 80}, {80, 24}} {
		for _, kind := range []string{"approve", "start", "resume", "answer", "stop"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], kind), func(t *testing.T) {
				f := inspectionFixture()
				m := model{}.selectMission(f.mission)
				r := m.journeyRecord()
				r.detail = &f.detail
				r.receipt = &f.receipt
				r.state = &f.state
				r.fresh = true
				r.sourceFresh = true
				var want any
				switch kind {
				case "approve":
					r.mode = "source"
					r.inspected = 100000
					want = f.detail.Identity
				case "start":
					want = domain.ExecutionRequest{Target: f.receipt.Identity, ApprovalID: f.receipt.ID}
				case "resume":
					want = domain.ResumeRequest{Target: f.receipt.Identity, ApprovalID: f.receipt.ID, Delivery: f.state.Delivery, OpenedDigest: f.state.OpenedDigest}
				case "answer":
					f.opened()
					f.question(1)
					r.state = &f.state
					r.question = f.state.Watch.PendingQuestion
					r.mode = "answer"
					r.focus = 1
					r.answer = strings.Repeat("answer\n\x1b\"ação", 80)
					want = r.question.Target.Answer(r.answer)
				case "stop":
					want = domain.ExecutionStopRequest{Target: f.state.Target, ApprovalID: f.state.ApprovalID, Delivery: f.state.Delivery, RunID: f.state.RunID}
				}
				m.putJourney(r)
				m = inspectionSize(t, m, size[0], size[1])
				if kind == "approve" {
					r = m.journeyRecord()
					r.inspected = 100000
					m.putJourney(r)
				}
				key := map[string]string{"approve": "y", "start": "e", "resume": "g", "answer": "enter", "stop": "z"}[kind]
				m, cmd := catalogKey(m, key)
				if cmd != nil {
					t.Fatal("confirmation dispatched")
				}
				if m.journeyRecord().confirm != kind {
					t.Fatal("confirmation not entered", m.journeyRecord().confirm)
				}
				inspectionCells(t, m)
				if !strings.Contains(m.View().Content, "pgup/pgdown") || !strings.Contains(m.View().Content, "enter confirm") {
					t.Fatal("paging/confirmation controls inaccessible", m.View().Content)
				}
				_, raw := inspectionDocument(t, m)
				expected, _ := json.MarshalIndent(want, "", "  ")
				if !bytes.Equal(raw, expected) {
					t.Fatalf("confirmation bytes differ\ngot %s\nwant %s", raw, expected)
				}
			})
		}
	}
}
func TestInspectionProgressRetainsCompleteStopHistory(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, size := range [][2]int{{240, 80}, {80, 24}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := inspectionFixture()
			f.opened()
			f.state.Watch.Header["evidence"] = strings.Repeat("evidence\n\x1b雪", 120)
			m := model{}.selectMission(f.mission)
			r := m.journeyRecord()
			r.state = &f.state
			r.watch = *f.state.Watch
			r.uncertain = "answer"
			for _, effect := range []domain.ExecutionStopEffect{domain.ExecutionStopPending, domain.ExecutionStopCompleted, domain.ExecutionStopUncertain} {
				req := &journeyRequest{kind: "stop"}
				result := domain.ExecutionStopResult{ExecutionStopRequest: domain.ExecutionStopRequest{Target: f.state.Target, ApprovalID: f.state.ApprovalID, Delivery: f.state.Delivery, RunID: f.state.RunID}, Effect: effect, Finalized: true, Durable: effect == domain.ExecutionStopCompleted, CoreState: "blocked", Causes: []string{strings.Repeat("cause\n\x1b雪", 100)}}
				out := journeyResult{request: req, stop: &result, effect: string(effect), err: fmt.Errorf("independent\n%s", strings.Repeat("long/", 100))}
				r.outcomes = append(r.outcomes, out)
				r.stopOutcome = &out
			}
			m.putJourney(r)
			m = inspectionSize(t, m, size[0], size[1])
			m = journeyKey(t, m, "w")
			before := m.journeyRecord()
			m, raw := inspectionDocument(t, m)
			var got struct {
				State *domain.ExecutionState `json:"execution"`
				Stops []struct {
					Result domain.ExecutionStopResult `json:"result"`
					Error  string                     `json:"error"`
					Latest bool                       `json:"latest_observation"`
				} `json:"stops"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.State, &f.state) || len(got.Stops) != 3 {
				t.Fatal("current execution/history missing", string(raw))
			}
			for i, out := range before.outcomes {
				if !reflect.DeepEqual(got.Stops[i].Result, *out.stop) || got.Stops[i].Error != out.err.Error() || got.Stops[i].Latest != (i == 2) {
					t.Fatal("retained observation changed", i)
				}
			}
			after := m.journeyRecord()
			if after.fresh != before.fresh || after.uncertain != before.uncertain || after.state != before.state || m.journey.epoch != 0 {
				t.Fatal("paging changed authority")
			}
		})
	}
}
func TestInspectionStopPagingDispatchesFrozenSelectionOnce(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := inspectionFixture()
	original := domain.ExecutionStopRequest{Target: f.state.Target, ApprovalID: f.state.ApprovalID, Delivery: f.state.Delivery, RunID: f.state.RunID}
	var bodies []domain.ExecutionStopRequest
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			var req domain.ExecutionStopRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			bodies = append(bodies, req)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopPending})
			return
		}
		handler(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = inspectionSize(t, m, 80, 24)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "z")
	m, raw := inspectionDocument(t, m)
	var got domain.ExecutionStopRequest
	if err := json.Unmarshal(raw, &got); err != nil || got != original {
		t.Fatal("frozen selection not inspectable", err)
	}
	f.state.RunID = strings.Repeat("b", 64)
	m = journeyFinish(t, m, m.readJourney("refresh"))
	m = inspectionSize(t, m, 240, 80)
	m = inspectionKey(t, m, "pgup")
	m = inspectionKey(t, m, "down")
	m, cmd := catalogKey(m, "enter")
	m, duplicate := catalogKey(m, "enter")
	if cmd == nil || duplicate != nil {
		t.Fatal("missing/duplicate command")
	}
	m = journeyFinish(t, m, cmd)
	m = journeyFinish(t, m, cmd)
	if !reflect.DeepEqual(bodies, []domain.ExecutionStopRequest{original}) {
		t.Fatal("retargeted/replayed", bodies)
	}
}

func TestInspectionSourcePhysicalBytesResizeAndReloadGate(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := inspectionFixture()
	f.detail.Approval = nil
	f.detail.Content = bytes.Repeat([]byte{0xff, 0x1b, 0, '\n', 0xe9, 0x9b, 0xaa}, 90)
	f.detail.Identity.Version = domain.PlanVersion(f.detail.Content)
	calls := 0
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			calls++
			var target domain.PlanIdentity
			_ = json.NewDecoder(r.Body).Decode(&target)
			if target != f.detail.Identity {
				t.Error("approval retargeted")
			}
			_ = json.NewEncoder(w).Encode(domain.PlanApprovalResult{Target: target, Effect: domain.PlanApprovalRecorded})
			return
		}
		_ = json.NewEncoder(w).Encode(f.detail)
	})
	m := model{api: c}.selectMission(f.mission)
	m = inspectionSize(t, m, 240, 80)
	m = journeyKey(t, m, "o")
	for range 1000 {
		if strings.Contains(m.View().Content, "END OF SOURCE") {
			break
		}
		m = inspectionKey(t, m, "down")
	}
	m = inspectionSize(t, m, 80, 24)
	m = journeyKey(t, m, "y")
	m = journeyKey(t, m, "enter")
	if calls != 0 || m.journeyRecord().confirm != "" {
		t.Fatal("old layout authorized unread bytes")
	}
	var meta, source []byte
	reading := false
	for range 1000 {
		rows := inspectionCells(t, m)
		end := false
		for _, row := range rows {
			if strings.HasPrefix(row, "Original source bytes;") {
				reading = true
			}
			if row == "END OF SOURCE" {
				end = true
			}
			if len(row) < 11 || row[8] != ' ' {
				continue
			}
			offset, err := strconv.ParseUint(row[:8], 16, 32)
			if err != nil {
				continue
			}
			part, err := strconv.Unquote(row[9:])
			if err != nil {
				t.Fatal(err)
			}
			dest := &meta
			if reading {
				dest = &source
			}
			if int(offset) != len(*dest) {
				t.Fatal("source inspection lost/duplicated bytes", offset, len(*dest))
			}
			*dest = append(*dest, part...)
		}
		if end {
			break
		}
		m = inspectionKey(t, m, "pgdown")
	}
	expected := f.detail
	expected.Content = nil
	expected.Approval = nil
	raw, _ := json.MarshalIndent(expected, "", "  ")
	if !bytes.Equal(meta, raw) || !bytes.Equal(source, f.detail.Content) {
		t.Fatal("metadata/original bytes changed")
	}
	m = journeyKey(t, m, "o")
	m = journeyKey(t, m, "y")
	if m.journeyRecord().confirm != "" {
		t.Fatal("reload inherited source coverage")
	}
	for range 1000 {
		if strings.Contains(m.View().Content, "END OF SOURCE") {
			break
		}
		m = inspectionKey(t, m, "down")
	}
	m = journeyKey(t, m, "y")
	m, _ = inspectionDocument(t, m)
	m = journeyKey(t, m, "enter")
	if calls != 1 {
		t.Fatal("fully inspected original bytes cannot be approved", calls)
	}
}

func TestInspectionPendingStopAndReceiptRemainReadable(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := inspectionFixture()
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			var req domain.ExecutionStopRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(domain.ExecutionStopResult{ExecutionStopRequest: req, Effect: domain.ExecutionStopPending})
			return
		}
		f.handler(t)(w, r)
	})
	m := model{api: c}.selectMission(f.mission)
	m = inspectionSize(t, m, 80, 24)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "z")
	m, cmd := catalogKey(m, "enter")
	if cmd == nil {
		t.Fatal("missing real pending Stop")
	}
	m = journeyKey(t, m, "w")
	pending := m.journey.pending
	_, raw := inspectionDocument(t, m)
	var progress struct {
		Pending *domain.ExecutionStopRequest `json:"pending_stop"`
	}
	if err := json.Unmarshal(raw, &progress); err != nil || progress.Pending == nil || *progress.Pending != *pending.stopTarget {
		t.Fatal("pending frozen request unavailable", err, string(raw))
	}
	if m.journey.pending != pending {
		t.Fatal("paging changed pending command")
	}
	m = journeyFinish(t, m, cmd)
	m = journeyKey(t, m, "esc")
	m = journeyKey(t, m, "k")
	_, raw = inspectionDocument(t, m)
	var receipt domain.PlanApproval
	if err := json.Unmarshal(raw, &receipt); err != nil || !reflect.DeepEqual(receipt, f.receipt) {
		t.Fatal("immutable receipt changed", err)
	}
}

func TestInspectionFrozenConfirmationNeverRetargetsOnRefresh(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := inspectionFixture()
	f.state.Active = false
	f.state.RunID = ""
	c := accountFixture(t, f.handler(t))
	m := model{api: c}.selectMission(f.mission)
	m = inspectionSize(t, m, 80, 24)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "e")
	_, before := inspectionDocument(t, m)
	r := m.journeyRecord()
	replacement := *r.receipt
	replacement.ID = "different-receipt"
	r.receipt = &replacement
	m.putJourney(r)
	_, after := inspectionDocument(t, m)
	if !bytes.Equal(before, after) {
		t.Fatal("confirmation silently retargeted")
	}
	m, cmd := catalogKey(m, "enter")
	if cmd != nil || len(f.writes) != 0 || m.journeyRecord().confirm != "start" {
		t.Fatal("changed selection dispatched")
	}
	epoch := m.journey.epoch
	m = journeyKey(t, m, "esc")
	if m.journeyRecord().confirm != "" || m.journey.epoch != epoch+1 || len(f.writes) != 0 {
		t.Fatal("Esc changed cancellation semantics")
	}
}

func TestInspectionProgressRefreshRestartsDocument(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := inspectionFixture()
	m := model{api: accountFixture(t, f.handler(t))}.selectMission(f.mission)
	m = inspectionSize(t, m, 80, 24)
	m = journeyKey(t, m, "f")
	m = journeyKey(t, m, "w")
	m = inspectionKey(t, m, "pgdown")
	if m.journeyRecord().page == 0 {
		t.Fatal("did not page")
	}
	f.state.RunID = strings.Repeat("b", 64)
	m = journeyFinish(t, m, m.readJourney("refresh"))
	if m.journeyRecord().page != 0 {
		t.Fatal("live progress update retained offsets from a different document")
	}
	_, raw := inspectionDocument(t, m)
	var got struct {
		State domain.ExecutionState `json:"execution"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got.State, f.state) {
		t.Fatal("new progress document cannot be reconstructed", err)
	}
}

func TestInspectionClientStopOutcomesReconstructExactly(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, effect := range []domain.ExecutionStopEffect{domain.ExecutionStopPending, domain.ExecutionStopCompleted, domain.ExecutionStopUncertain} {
		t.Run(string(effect), func(t *testing.T) {
			f := inspectionFixture()
			f.opened()
			var result domain.ExecutionStopResult
			calls := 0
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/stop") {
					f.handler(t)(w, r)
					return
				}
				calls++
				var request domain.ExecutionStopRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				result = domain.ExecutionStopResult{ExecutionStopRequest: request, Effect: effect, Causes: []string{strings.Repeat("cause\n雪\x1b/", 100)}, CoreState: "blocked"}
				switch effect {
				case domain.ExecutionStopPending:
					result.Causes = nil
					result.CoreState = ""
					w.WriteHeader(202)
				case domain.ExecutionStopCompleted:
					state := f.state
					state.Active = false
					state.RunID = ""
					state.Watch.Header["state"] = "blocked"
					result.State = &state
					result.Finalized = true
					result.Durable = true
				case domain.ExecutionStopUncertain:
					result.Finalized = true
					result.Error = &domain.PlanFailure{Code: "uncertain", Message: "durability unknown"}
					w.WriteHeader(503)
				}
				_ = json.NewEncoder(w).Encode(result)
			})
			m := model{api: c}.selectMission(f.mission)
			m = inspectionSize(t, m, 80, 24)
			m = journeyKey(t, m, "f")
			m = journeyKey(t, m, "z")
			m = journeyKey(t, m, "enter")
			if calls != 1 {
				t.Fatal("missing Stop dispatch")
			}
			record := m.journeyRecord()
			m = journeyKey(t, m, "w")
			m, raw := inspectionDocument(t, m)
			var progress struct {
				Stops []struct {
					Result domain.ExecutionStopResult `json:"result"`
					Error  string                     `json:"error"`
				} `json:"stops"`
			}
			if err := json.Unmarshal(raw, &progress); err != nil || len(progress.Stops) != 1 || !reflect.DeepEqual(progress.Stops[0].Result, result) {
				t.Fatal("client Stop result not losslessly inspectable", err, string(raw))
			}
			expectedError := ""
			if record.outcomes[0].err != nil {
				expectedError = record.outcomes[0].err.Error()
			}
			if progress.Stops[0].Error != expectedError || m.journeyRecord().stopUnresolved() != record.stopUnresolved() || calls != 1 {
				t.Fatal("independent error/uncertainty/replay changed")
			}
		})
	}
}

func TestInspectionProgressShortcutVisibleAt80Columns(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	f := inspectionFixture()
	m := model{}.selectMission(f.mission)
	m = inspectionSize(t, m, 80, 24)
	rows := strings.Split(m.View().Content, "\n")
	var cells strings.Builder
	for _, row := range rows[:min(len(rows), 24)] {
		runes := []rune(row)
		cells.WriteString(string(runes[:min(len(runes), 80)]))
		cells.WriteByte('\n')
	}
	if !strings.Contains(cells.String(), "w full progress/evidence") {
		t.Fatal("existing w inspection route is hidden beyond terminal columns")
	}
}

func TestInspectionAttachedTerminalPhysicalBounds(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, mode := range []string{"confirmation", "source", "receipt", "progress"} {
		t.Run(mode, func(t *testing.T) {
			f := inspectionFixture()
			m := model{}.selectMission(f.mission)
			m.sessID = "attached-session"
			m.validMsg = "retained validation notice"
			m.termFrame = terminalFrame{text: strings.Repeat("attached terminal row\n", 14), cols: 80, rows: 24, owned: true, owner: "original-owner", control: "retained ownership warning", provisional: true, held: true, problem: "retained terminal warning"}
			m.catalog.detail = "retained catalog notice"
			m.account.problem = "account_read_failed"
			m.workspace.problem = "workspace_read_failed"
			m.workspace.detail = "retained workspace detail"
			r := m.journeyRecord()
			r.detail, r.receipt, r.historical, r.state = &f.detail, &f.receipt, &f.receipt, &f.state
			r.fresh, r.sourceFresh = true, true
			r.mode = mode
			if mode == "confirmation" {
				r.mode, r.confirm = "", "planning"
			}
			m.putJourney(r)
			m = inspectionSize(t, m, 80, 24)
			frame, catalog, account, workspace := m.termFrame, m.catalog, m.account, m.workspace
			session, valid, termMode := m.sessID, m.validMsg, m.termMode
			// Every page must fit, including the outer model.View frame.
			if mode == "source" {
				for n := 0; n < 10000; n++ {
					rows := inspectionCells(t, m)
					if strings.Contains(strings.Join(rows, "\n"), "END OF SOURCE") {
						break
					}
					m = inspectionKey(t, m, "pgdown")
					if n == 9999 {
						t.Fatal("source never ended")
					}
				}
			} else {
				var raw []byte
				m, raw = inspectionDocument(t, m)
				var want []byte
				r = m.journeyRecord()
				switch mode {
				case "confirmation":
					want = r.confirmData
				case "receipt":
					want, _ = json.MarshalIndent(r.historical, "", "  ")
				case "progress":
					var v map[string]json.RawMessage
					if err := json.Unmarshal(raw, &v); err != nil || string(v["mission"]) != strconv.Quote(f.mission.ID) {
						t.Fatal("progress lost its mission", err)
					}
					want = raw
				}
				if !bytes.Equal(raw, want) {
					t.Fatal("attached frame changed inspected bytes")
				}
			}
			assertRetained := func() {
				t.Helper()
				if !reflect.DeepEqual(m.termFrame, frame) || !reflect.DeepEqual(m.catalog, catalog) || !reflect.DeepEqual(m.account, account) || !reflect.DeepEqual(m.workspace, workspace) || m.sessID != session || m.validMsg != valid || m.termMode != termMode {
					t.Fatal("inspection changed the attached session or retained notices")
				}
			}
			assertRetained()
			var cmd tea.Cmd
			m, cmd = catalogKey(m, "esc")
			if cmd != nil {
				t.Fatal("leaving inspection dispatched a command")
			}
			assertRetained()
			view := m.View().Content
			for _, text := range []string{"attached terminal row", valid, "retained catalog notice", i18n.T("account_read_failed"), i18n.T("workspace_read_failed")} {
				if !strings.Contains(view, text) {
					t.Fatalf("leaving inspection did not restore %q", text)
				}
			}
		})
	}
}

func TestInspectionHeaderRetainsMissionContext(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, kind := range []string{"planning", "new_answer", "answer", "source", "receipt", "progress"} {
		t.Run(kind, func(t *testing.T) {
			f := inspectionFixture()
			f.mission.ID = "mission-context\n\x1b"
			f.mission.WorkspaceID = "workspace-context\r\x00"
			m := model{}.selectMission(f.mission)
			r := m.journeyRecord()
			r.detail, r.receipt, r.historical, r.state = &f.detail, &f.receipt, &f.receipt, &f.state
			if kind == "source" || kind == "receipt" || kind == "progress" {
				r.mode = kind
			} else {
				r.confirm = kind
			}
			m.putJourney(r)
			m = inspectionSize(t, m, 80, 24)
			rows := inspectionCells(t, m)
			view := strings.Join(rows, "\n")
			for _, id := range []string{f.mission.ID, f.mission.WorkspaceID} {
				if !strings.Contains(view, strconv.QuoteToASCII(id)) {
					t.Fatalf("inspection omits escaped context %q", id)
				}
			}
		})
	}
}

func TestInspectionPendingSourceCannotHideFocusedChild(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	m, manager := terminalAttachedFixture(t)
	if !m.link.offer(terminalAction{operation: "tui"}) {
		t.Fatal("explicit Take was not admitted")
	}
	m.termFrame = terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return f.owned })
	frame, link, session := m.termFrame, m.link, m.sessID
	f := inspectionFixture()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	messages := make(chan tea.Msg, 1)
	handler := f.handler(t)
	c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/plan") {
			close(entered)
			<-release
		}
		handler(w, r)
	})
	m = m.selectMission(f.mission)
	m.api = c
	m = inspectionSize(t, m, 80, 24)
	m, read := catalogKey(m, "o")
	if read == nil || m.journey.pending == nil || m.journey.pending.kind != "source" {
		t.Fatal("source request was not scheduled")
	}
	go func() { defer close(finished); messages <- read() }()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		terminalFixtureAwait(t, terminalFixtureTestContext(t), finished)
	})
	terminalFixtureAwait(t, terminalFixtureTestContext(t), entered)
	var cmd tea.Cmd
	m, cmd = catalogKey(m, "i")
	if cmd != nil || m.termMode {
		t.Fatal("pending source allowed child focus before an incoming inspection")
	}
	if m.link != link || m.sessID != session || m.termFrame != frame {
		t.Fatal("pending read changed attachment or authority")
	}
	close(release)
	released = true
	message := terminalFixtureAwait(t, terminalFixtureTestContext(t), messages)
	next, cmd := m.Update(message)
	m = next.(model)
	if cmd != nil || m.termMode || m.journeyRecord().mode != "source" {
		t.Fatal("source response changed focus or did not open inspection")
	}
	inspectionCells(t, m)
	m = inspectionKey(t, m, "pgdown")
	if m.journeyRecord().page == 0 {
		t.Fatal("inspection paging did not consume the key")
	}
	m, cmd = catalogKey(m, "esc")
	if cmd != nil || m.journeyRecord().mode != "" || m.link != link || m.sessID != session || m.termFrame != frame {
		t.Fatal("Escape did not retain the actual attachment")
	}
	m, cmd = catalogKey(m, "i")
	if cmd != nil || !m.termMode {
		t.Fatal("explicit child focus was not available after inspection")
	}
	m, cmd = catalogKey(m, "VISIBLE_INSPECTION_FOCUS_INPUT")
	if cmd != nil {
		t.Fatal("child input unexpectedly scheduled a journey command")
	}
	terminalAwaitFrame(t, m.link, func(f terminalFrame) bool { return strings.Contains(f.text, "VISIBLE_INSPECTION_FOCUS_INPUT") })
	other, ok := manager.Snapshot("a-unrelated")
	if !ok || other.OwnerClient != "" || other.Process != "running" {
		t.Fatal("inspection or child focus affected another session", other)
	}
}

// Real API/Service/Store/Core projection, without a listener or provider process.
type inspectionAPITransport struct{ handler http.Handler }

func (p inspectionAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, req)
	return w.Result(), nil
}

func inspectionProjectionFixture(t *testing.T) model {
	t.Helper()
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
	svc := service.New(st, nil, nil)
	check(st.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}))
	check(st.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}))
	_, err = svc.BindMission("m", service.MissionBindingRequest{WorkspaceID: "w", PlanPath: path})
	check(err)
	c := client.New("http://inspection.invalid", "inspection")
	c.HTTP.Transport = inspectionAPITransport{(&api.Server{Svc: svc, Token: "inspection"}).Handler()}
	detail, err := c.PlanDetail(context.Background(), "m", "w")
	check(err)
	approved, err := c.ApprovePlan(context.Background(), detail.Identity)
	check(err)
	if approved.Receipt == nil {
		t.Fatal("missing real approval receipt")
	}
	a := *approved.Receipt
	b := a.Identity.Binding
	b.DeliveryID = domain.ExecutionDelivery(a.ID, a.Identity)
	js, err := journal.Open(b.CanonicalRoot)
	check(err)
	opening, err := json.Marshal(map[string]any{"slug": b.PlanSlug, "plan_path": b.PlanPath, "plan_digest": b.PlanDigest, "workspace": b.CanonicalRoot, "approval_receipt_id": a.ID, "plan_content_digest": a.Identity.Version, "supervision": true})
	check(err)
	opened, err := js.Append(b.DeliveryID, journal.Record{Kind: loop.KindOpened, Detail: opening, Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
	check(err)
	b.OpenedDigest = opened.Digest
	_, err = js.Append(b.DeliveryID, journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"done"}`), Graph: opened.Graph})
	check(err)
	check(st.BindMission("m", b))
	mission, err := st.GetMission("m")
	check(err)
	m := inspectionSize(t, model{api: c}.selectMission(mission), 80, 24)
	m = journeyKey(t, m, "f")
	r := m.journeyRecord()
	if !r.fresh || r.state == nil || r.state.Active || r.state.Watch == nil || r.state.Watch.Header["state"] != loop.StateReviewBlocked || r.state.Gate == nil || !r.state.Gate.Required || r.state.Gate.Cleared {
		t.Fatal("real completed projection unavailable", r.problemErr, r.state)
	}
	t.Log("seeded terminal journal; actual client/API/Service/Store/Core projection; no native execution or supervision qualification")
	return m
}

// Run the real one-second timer under synctest's virtual clock, then the journey
// command selected by Update. Unrelated dashboard commands are not executed.
func inspectionBackgroundTick(t *testing.T, m model) model {
	t.Helper()
	next, _ := m.Update(m.tick()())
	m = next.(model)
	if req := m.journey.pending; req != nil && req.kind == "refresh" {
		m = journeyFinish(t, m, req.run)
	}
	return m
}

func TestInspectionProgressSnapshotAcrossBackgroundTicks(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	synctest.Test(t, func(t *testing.T) {
		m := inspectionProjectionFixture(t)
		before := m.journeyRecord()
		m = inspectionBackgroundTick(t, m)
		after := m.journeyRecord()
		oldAge := before.state.Watch.Health["journal_age_ns"]
		newAge := after.state.Watch.Health["journal_age_ns"]
		if oldAge != float64(0) || newAge != float64(time.Second) {
			t.Fatalf("controlled real projection age: %v -> %v", oldAge, newAge)
		}
		oldJSON, _ := json.Marshal(before.state)
		newJSON, _ := json.Marshal(after.state)
		if !bytes.Equal(bytes.ReplaceAll(newJSON, []byte(`"journal_age_ns":1000000000`), []byte(`"journal_age_ns":0`)), oldJSON) {
			t.Fatal("business facts changed in age-only control")
		}
		t.Logf("accepted normal tick: embedded journal_age_ns %v -> %v; all other execution bytes equal", oldAge, newAge)
		m = journeyKey(t, m, "w")
		_, expected := inspectionDocument(t, m)
		var raw []byte
		for page := 0; page < 10000; page++ {
			beforeFrame, beforePage := m.View().Content, m.journeyRecord().page
			for tick := 0; page > 0 && tick < 3; tick++ {
				m = inspectionBackgroundTick(t, m)
			}
			if m.journeyRecord().page != beforePage || m.View().Content != beforeFrame {
				t.Fatalf("normal background ticks replaced inspected document: page %d -> %d, age %v -> %v", beforePage, m.journeyRecord().page, newAge, m.journeyRecord().state.Watch.Health["journal_age_ns"])
			}
			end := false
			for _, row := range inspectionCells(t, m) {
				if row == "END OF SOURCE" {
					end = true
				}
				if len(row) < 11 || row[8] != ' ' {
					continue
				}
				offset, err := strconv.ParseUint(row[:8], 16, 32)
				if err != nil {
					continue
				}
				part, err := strconv.Unquote(row[9:])
				if err != nil || int(offset) != len(raw) {
					t.Fatalf("mixed/lost bytes at %d, reconstructed %d: %v", offset, len(raw), err)
				}
				raw = append(raw, part...)
			}
			if end {
				if page < 2 || !bytes.Equal(raw, expected) {
					t.Fatal("full multipage snapshot differs")
				}
				t.Logf("reconstructed %d bytes over %d pages and %d controlled one-second ticks through END", len(raw), page+1, 3*page)
				return
			}
			m = inspectionKey(t, m, "pgdown")
		}
		t.Fatal("missing END")
	})
}

func TestInspectionProgressSnapshotRefreshOrderingAndFreshRead(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, ordering := range []string{"pending", "queued", "applied"} {
		t.Run(ordering, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := inspectionProjectionFixture(t)
				next, _ := m.Update(m.tick()())
				m = next.(model)
				req := m.journey.pending
				if req == nil || req.kind != "refresh" {
					t.Fatal("ordinary tick did not schedule refresh")
				}
				var message tea.Msg
				if ordering != "pending" {
					message = req.run()
				}
				if ordering == "applied" {
					m = journeyFinish(t, m, func() tea.Msg { return message })
				}
				m = journeyKey(t, m, "w")
				m = inspectionKey(t, m, "pgdown")
				frame, record := m.View().Content, m.journeyRecord()
				if m.journey.pending != nil {
					t.Fatal("opening progress did not detach pending refresh")
				}
				if ordering == "pending" {
					message = req.run()
				}
				m = journeyFinish(t, m, func() tea.Msg { return message })
				m = inspectionBackgroundTick(t, m)
				if m.View().Content != frame || !reflect.DeepEqual(m.journeyRecord(), record) {
					t.Fatal("late/duplicate refresh replaced the snapshot")
				}
				epoch := m.journey.epoch
				m = journeyKey(t, m, "esc")
				if m.journeyRecord().mode != "" || m.journeyRecord().fresh || m.journeyRecord().sourceFresh || m.journey.epoch != epoch+1 {
					t.Fatal("Esc did not retain cancellation/freshness rules")
				}
				m = inspectionBackgroundTick(t, m)
				if !m.journeyRecord().fresh || m.journeyRecord().state.Watch.Health["journal_age_ns"] != float64(3*time.Second) {
					t.Fatal("Esc did not resume ordinary refresh")
				}
				// Advance the real timer without delivering its background message:
				// f must independently fetch the newer projection before w freezes it.
				m.tick()()
				m = journeyKey(t, m, "f")
				m = journeyKey(t, m, "w")
				m, raw := inspectionDocument(t, m)
				if !bytes.Contains(raw, []byte(`"journal_age_ns": 4000000000`)) || m.journeyRecord().page == 0 {
					t.Fatal("explicit f+w did not expose a complete fresh snapshot")
				}
			})
		})
	}
}

func TestInspectionProgressSnapshotAppliedBusinessResult(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	synctest.Test(t, func(t *testing.T) {
		m := inspectionProjectionFixture(t)
		m = journeyKey(t, m, "w")
		m = inspectionKey(t, m, "pgdown")
		r := m.journeyRecord()
		js, err := journal.Open(r.state.Target.Binding.CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		_, err = js.Append(r.state.Delivery, journal.Record{Kind: loop.KindTerminal, Detail: json.RawMessage(`{"state":"canceled"}`), Graph: json.RawMessage(`{"tasks":[],"waves":[]}`)})
		if err != nil {
			t.Fatal(err)
		}
		frame := m.View().Content
		m = inspectionBackgroundTick(t, m)
		if m.View().Content != frame {
			t.Fatal("background refresh replaced snapshot after external business change")
		}
		cmd := m.readJourney("refresh")
		if cmd == nil {
			t.Fatal("explicit refresh was suppressed")
		}
		m = journeyFinish(t, m, cmd)
		if m.journeyRecord().page != 0 || m.journeyRecord().state.Watch.Header["state"] != "canceled" {
			t.Fatal("applied real business result did not restart the document")
		}
		_, raw := inspectionDocument(t, m)
		var got struct {
			State domain.ExecutionState `json:"execution"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got.State, *m.journeyRecord().state) {
			t.Fatal("restarted business document lost bytes", err)
		}
	})
}

func TestInspectionProgressSnapshotAppliesQueuedStopResults(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, effect := range []domain.ExecutionStopEffect{domain.ExecutionStopPending, domain.ExecutionStopCompleted, domain.ExecutionStopUncertain} {
		t.Run(string(effect), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := inspectionFixture()
				f.opened()
				calls := 0
				var result domain.ExecutionStopResult
				c := accountFixture(t, func(w http.ResponseWriter, request *http.Request) {
					if !strings.HasSuffix(request.URL.Path, "/stop") {
						f.handler(t)(w, request)
						return
					}
					calls++
					var target domain.ExecutionStopRequest
					if err := json.NewDecoder(request.Body).Decode(&target); err != nil {
						t.Error(err)
					}
					result = domain.ExecutionStopResult{ExecutionStopRequest: target, Effect: effect}
					switch effect {
					case domain.ExecutionStopPending:
						w.WriteHeader(http.StatusAccepted)
					case domain.ExecutionStopCompleted:
						state := f.state
						state.Active, state.RunID = false, ""
						watch := *state.Watch
						watch.Header = map[string]any{"state": "blocked"}
						state.Watch = &watch
						result.State = &state
						result.Finalized, result.Durable = true, true
						result.CoreState = "blocked"
						result.Causes = []string{"retained cancellation evidence"}
					case domain.ExecutionStopUncertain:
						result.Finalized = true
						result.Error = &domain.PlanFailure{Code: "uncertain", Message: "durability unknown"}
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					_ = json.NewEncoder(w).Encode(result)
				})
				m := inspectionSize(t, model{api: c}.selectMission(f.mission), 80, 24)
				m = journeyKey(t, m, "f")
				r := m.journeyRecord()
				r.uncertain, r.problem = "answer", "journey_uncertain"
				r.problemErr = fmt.Errorf("original answer outcome unknown")
				m.putJourney(r)
				m = journeyKey(t, m, "z")
				m, cmd := catalogKey(m, "enter")
				if cmd == nil || m.journey.pending == nil || m.journey.pending.stopTarget == nil {
					t.Fatal("Stop was not dispatched")
				}
				target := *m.journey.pending.stopTarget
				// Execute the actual client command, retaining its asynchronous Update
				// message until inspection has opened and several ticks have passed.
				message := cmd()
				m = journeyKey(t, m, "w")
				m, pendingRaw := inspectionDocument(t, m)
				var pending struct {
					Target *domain.ExecutionStopRequest `json:"pending_stop"`
				}
				if err := json.Unmarshal(pendingRaw, &pending); err != nil || pending.Target == nil || *pending.Target != target {
					t.Fatal("pending Stop target disappeared", err)
				}
				frame := m.View().Content
				for tick := 0; tick < 3; tick++ {
					m = inspectionBackgroundTick(t, m)
				}
				if m.View().Content != frame || m.journey.pending == nil {
					t.Fatal("ticks replaced pending Stop snapshot")
				}
				m = journeyFinish(t, m, func() tea.Msg { return message })
				after := m.journeyRecord()
				if m.journey.pending != nil || after.page != 0 || len(after.outcomes) != 1 || after.stopOutcome == nil || !reflect.DeepEqual(*after.stopOutcome.stop, result) || calls != 1 {
					t.Fatal("queued Stop result did not apply/reset exactly once", after.stopOutcome)
				}
				if after.uncertain != r.uncertain || after.problem != r.problem || after.problemErr != r.problemErr || *after.stopSelection != target {
					t.Fatal("Stop result hid original uncertainty or frozen selection")
				}
				if effect == domain.ExecutionStopCompleted {
					if after.fresh || after.state.Active || !reflect.DeepEqual(after.state, result.State) {
						t.Fatal("completed Stop failed state/freshness invalidation")
					}
				} else if !after.stopUnresolved() || after.state != r.state || after.fresh != r.fresh {
					t.Fatal("pending/uncertain Stop changed execution authority")
				}
				if effect == domain.ExecutionStopUncertain && (after.stopOutcome.err == nil || !strings.Contains(after.stopOutcome.err.Error(), "durability unknown")) {
					t.Fatal("client uncertainty error disappeared")
				}
				_, raw := inspectionDocument(t, m)
				var got struct {
					Pending *domain.ExecutionStopRequest `json:"pending_stop"`
					Stops   []struct {
						Result domain.ExecutionStopResult `json:"result"`
						Error  string                     `json:"error"`
					} `json:"stops"`
				}
				if err := json.Unmarshal(raw, &got); err != nil || got.Pending != nil || len(got.Stops) != 1 || !reflect.DeepEqual(got.Stops[0].Result, result) {
					t.Fatal("Stop document did not reconstruct result", err)
				}
				if after.stopOutcome.err != nil && got.Stops[0].Error != after.stopOutcome.err.Error() {
					t.Fatal("Stop document lost independent error")
				}
				frame = m.View().Content
				m = inspectionBackgroundTick(t, m)
				m = journeyFinish(t, m, func() tea.Msg { return message })
				if m.View().Content != frame || len(m.journeyRecord().outcomes) != 1 || calls != 1 {
					t.Fatal("background tick or duplicate result changed retained Stop")
				}
			})
		})
	}
}
