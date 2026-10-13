package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestRecoveryDaemonChild(t *testing.T) {
	if os.Getenv("CONSOLE_RECOVERY_CHILD") != "1" {
		return
	}
	os.Args = []string{"consoled", "-home", os.Getenv("CONSOLE_RECOVERY_HOME"), "-listen", "127.0.0.1:invalid-port"}
	main()
}

// An invalid port makes the real entrypoint exit at listener creation, even in
// a bind-restricted sandbox. Recovery must already have committed at that point.
func TestStartupRecoversBeforeListenerWithoutGET(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "event-failure"}[reject], func(t *testing.T) {
			home := testfixture.PrivateHome(t)
			path := filepath.Join(home, "console.db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			original := domain.Session{ID: "old", Process: "running", Owner: "web", OwnerClient: "old-client", PID: 0, CreatedAt: domain.Now()}
			if err := st.PutSession(original); err != nil {
				t.Fatal(err)
			}
			st.Close()
			if reject {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`CREATE TRIGGER reject_recovery BEFORE INSERT ON events WHEN NEW.kind='session.reconciled' BEGIN SELECT RAISE(ABORT,'recovery rejected'); END`)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryDaemonChild$")
			cmd.Env = append(os.Environ(), "CONSOLE_RECOVERY_CHILD=1", "CONSOLE_RECOVERY_HOME="+home)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("invalid port must fail")
			}
			st, err = store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			got, err := st.GetSession("old")
			if err != nil {
				t.Fatal(err)
			}
			events, err := st.EventsSince(0, 100)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, ev := range events {
				if ev.Kind == "session.reconciled" {
					count++
				}
			}
			if reject {
				if !strings.Contains(string(output), "recovery rejected") || strings.Contains(string(output), "invalid-port") {
					t.Errorf("recovery error did not stop startup before listener: %s", output)
				}
				if got.Process != "running" || count != 0 {
					t.Fatalf("failed transaction changed catalog: %+v %+v", got, events)
				}
			} else {
				if !strings.Contains(string(output), "invalid-port") {
					t.Fatalf("did not reach listener: %s", output)
				}
				if got.Process != "unknown" || got.Owner != "none" || got.OwnerClient != "" || got.EndedAt != nil || count != 1 {
					t.Fatalf("startup reached listener with unrecovered history: %+v, events=%+v; output=%s", got, events, output)
				}
			}
		})
	}
}
