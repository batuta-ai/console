//go:build darwin || linux

package tui

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/store"
)

func TestOuterNativeRefreshPreservesOperationCaption(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
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
		st, err := store.Open(filepath.Join(t.TempDir(), "refresh.db"))
		check(err)
		t.Cleanup(func() { check(st.Close()) })
		workspace := domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}
		check(st.PutWorkspace(workspace))
		mission := domain.Mission{ID: "m", WorkspaceID: workspace.ID, CreatedAt: domain.Now()}
		check(st.PutMission(mission))
		handler := (&api.Server{Svc: service.New(st, nil, nil), Token: "inspection"}).Handler()
		var mutations int
		c := client.New("http://inspection.invalid", "inspection")
		c.HTTP.Transport = inspectionAPITransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				mutations++
			}
			handler.ServeHTTP(w, r)
		})}
		m := inspectionSize(t, model{api: c}.selectMission(mission), 240, 80)
		m = journeyKey(t, m, "b")
		m = catalogPaste(m, path)
		m = journeyKey(t, m, "tab")
		m = journeyKey(t, m, "tab")
		m = journeyKey(t, m, "enter")
		m = journeyKey(t, m, "enter")
		m = journeyKey(t, m, "o")
		m = journeyInspectAll(t, m)
		m = journeyKey(t, m, "y")
		m = journeyKey(t, m, "enter")
		r := m.journeyRecord()
		if r.receipt == nil || r.effect != "recorded" || mutations != 2 {
			t.Fatal("public keyboard approval did not record an operation", m.View().Content, mutations)
		}
		want := outerNativeMission{workspace: workspace, mission: mission, approval: *r.receipt}
		ready := func(s string) bool {
			return !strings.Contains(s, "Request failed") && strings.Contains(s, "Last verified effect: read_only") && strings.Contains(s, "Immutable approved snapshot "+want.approval.ID+" "+want.approval.Identity.Version) && !strings.Contains(s, "refresh required")
		}
		m = journeyKey(t, m, "w")
		inspection := m.View().Content
		m = journeyKey(t, m, "esc")
		stale := m.View().Content
		if !strings.Contains(stale, "refresh required") {
			t.Fatal("Esc did not invalidate facts", stale)
		}
		m = inspectionBackgroundTick(t, m)
		frame := m.View().Content
		r = m.journeyRecord()
		actual, err := c.ExecutionState(t.Context(), mission.ID, workspace.ID, want.approval.ID)
		check(err)
		if !r.fresh || r.effect != "recorded" || !reflect.DeepEqual(r.state, &actual) || !strings.Contains(frame, "Last verified effect: recorded") || actual.Active || actual.Bound || mutations != 2 {
			t.Fatal("periodic read lost public current facts or operation", frame)
		}
		if !outerNativeRefreshFrame(frame, want) {
			t.Fatal("fresh periodic frame with historical caption rejected", frame)
		}
		if ready(frame) {
			t.Fatal("automatic read satisfied explicit read_only guard", frame)
		}
		for name, unsafe := range map[string]string{
			"mission":      strings.ReplaceAll(frame, "m  Workspace: w", "other  Workspace: w"),
			"workspace":    strings.ReplaceAll(frame, "m  Workspace: w", "m  Workspace: other"),
			"mode":         inspection,
			"confirmation": frame + "\nConfirm: Stop",
			"pending":      frame + "\nMission request pending",
		} {
			if outerNativeRefreshFrame(unsafe, want) {
				t.Fatal("unsafe boundary accepted", name, unsafe)
			}
		}
		m = journeyKey(t, m, "f")
		frame = m.View().Content
		if !outerNativeRefreshFrame(frame, want) || !ready(frame) || mutations != 2 {
			t.Fatal("explicit f did not establish current read_only receipt", frame)
		}
		for name, unsafe := range map[string]string{
			"stale":   frame + "\nrefresh required",
			"failed":  frame + "\nRequest failed",
			"receipt": strings.ReplaceAll(frame, want.approval.ID, "other"),
			"version": strings.ReplaceAll(frame, want.approval.Identity.Version, "other"),
			"effect":  strings.ReplaceAll(frame, "Last verified effect: read_only", "Last verified effect: recorded"),
		} {
			if ready(unsafe) {
				t.Fatal("unsafe final facts accepted", name)
			}
		}
	})
}
