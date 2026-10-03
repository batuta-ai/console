package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestLateInitialSessionWritePreservesExit(t *testing.T) {
	for _, kind := range []string{"session.started", "account.login", "workspace.init", "bare"} {
		t.Run(kind, func(t *testing.T) {
			st, err := Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			initial := domain.Session{ID: "s", Process: "running", PID: 42, CreatedAt: domain.Now()}
			ended := time.Date(2026, 10, 3, 0, 0, 0, 123, time.UTC)
			// Every field except the lifecycle ID differs from the stale write.
			terminal := domain.Session{
				ID: initial.ID, MissionID: "mission", WorkspaceID: "workspace",
				MemberID: "member", AccountID: "account", Adapter: "codex",
				Argv: []string{"codex", "terminal-argument"}, Process: "exited", Work: "complete",
				Owner: "tui", OwnerClient: "terminal-client", PID: 84, Cols: 90, Rows: 30,
				BatutaInjected: true, SkillsVersion: "terminal-version", ProcessIdentity: "observed-terminal", RecoveryReason: "historical",
				CreatedAt: ended.Add(-time.Minute), EndedAt: &ended,
			}
			if err := st.PutSessionWithEvent(terminal, "session.exited", map[string]any{"id": "s"}); err != nil {
				t.Fatal(err)
			}
			if kind == "bare" {
				err = st.PutSession(initial)
			} else {
				err = st.PutSessionWithEvent(initial, kind, map[string]any{"id": "s"})
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.GetSession("s")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, terminal) {
				t.Fatalf("late write changed terminal session:\ngot  %+v\nwant %+v", got, terminal)
			}
			events, err := st.EventsSince(0, 10)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if kind == "bare" {
				want = 1
			}
			if len(events) != want {
				t.Fatalf("events: %+v", events)
			}
		})
	}
}
