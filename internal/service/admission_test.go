package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

type admissionFixture struct {
	svc       *Service
	db        *sql.DB
	account   domain.Account
	workspace domain.Workspace
	mission   domain.Mission
	launches  string
}

func admissionSetup(t *testing.T, cap int) admissionFixture {
	t.Helper()
	svc, db := persistenceSvc(t)
	t.Setenv("BATUTA_SKILLS", t.TempDir())
	cli := t.TempDir()
	launches := filepath.Join(cli, "launches")
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" >> '" + launches + "'\nwhile IFS= read -r line; do printf 'ack:%s\\n' \"$line\"; done\n"
	if err := os.WriteFile(filepath.Join(cli, "codex"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cli)
	w, err := svc.CreateWorkspace("w", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q, err := svc.SaveSquad(domain.Squad{Name: "q"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.CreateMission(w.ID, "m", "", q.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.CreateAccount("a", "codex")
	if err != nil {
		t.Fatal(err)
	}
	a.SessionCap = cap
	if err := svc.Store.PutAccount(a); err != nil {
		t.Fatal(err)
	}
	a, err = svc.ConnectAccount(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return admissionFixture{svc, db, a, w, m, launches}
}

func (f admissionFixture) start() (domain.Session, error) {
	off := false
	return f.svc.StartSession(StartSessionIn{MissionID: f.mission.ID, AccountID: f.account.ID, Inject: &off, Owner: "web", Client: "test"})
}

// PTY input/output is a handshake with the disposable process, so launch-file
// assertions never depend on the scheduler or sleeps.
// This helper takes ownership for reuse tests; fresh ownership is tested without it.
func readyProcess(t *testing.T, svc *Service, sess domain.Session) {
	t.Helper()
	done := make(chan struct{}, 1)
	var outputMu sync.Mutex
	var output []byte
	unsub, err := svc.Session.Subscribe(sess.ID, func(b []byte) {
		outputMu.Lock()
		defer outputMu.Unlock()
		output = append(output, b...)
		if strings.Contains(string(output), "ack:ready") {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if _, err := svc.Session.Take(sess.ID, "web", "test"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Session.Input(sess.ID, "test", []byte("ready\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not acknowledge input")
	}
}

func TestConcurrentAccountAdmission(t *testing.T) {
	f := admissionSetup(t, 3)
	gate := make(chan struct{})
	type result struct {
		sess domain.Session
		err  error
	}
	results := make(chan result, 10)
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-gate
			sess, err := f.start()
			results <- result{sess, err}
		}()
	}
	ready.Wait()
	close(gate)
	wg.Wait()
	close(results)
	var live []domain.Session
	for r := range results {
		if r.err == nil {
			live = append(live, r.sess)
		} else if !strings.Contains(r.err.Error(), "capacity") {
			t.Errorf("unexpected admission error: %v", r.err)
		}
	}
	for _, sess := range live {
		readyProcess(t, f.svc, sess)
	}
	// Read the shell's launch evidence after its command loop acknowledges input.
	for _, sess := range live {
		if _, err := f.svc.StopSession(sess.ID); err != nil {
			t.Error(err)
		}
	}
	data, err := os.ReadFile(f.launches)
	if err != nil {
		t.Fatal(err)
	}
	count := len(strings.Fields(string(data)))
	t.Logf("requests=10 cap=3 successful=%d real process launches=%d", len(live), count)
	if len(live) != 3 || count != 3 {
		t.Fatalf("capacity exceeded or underfilled: successes=%d launches=%d", len(live), count)
	}
}

func TestAccountCapacitySharedWithLogin(t *testing.T) {
	for _, first := range []string{"session", "login"} {
		t.Run(first, func(t *testing.T) {
			f := admissionSetup(t, 1)
			var live domain.Session
			var err error
			if first == "session" {
				live, err = f.start()
			} else {
				live, err = f.svc.StartLogin(f.account.ID, "web", "test")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer f.svc.StopSession(live.ID)
			var extra domain.Session
			if first == "session" {
				extra, err = f.svc.StartLogin(f.account.ID, "web", "test")
			} else {
				extra, err = f.start()
			}
			if extra.PID != 0 {
				defer f.svc.StopSession(extra.ID)
			}
			if err == nil || !strings.Contains(err.Error(), "capacity") {
				t.Fatalf("shared capacity: %v", err)
			}
		})
	}
}

func TestAccountAdmissionFailuresRelease(t *testing.T) {
	for _, failure := range []string{"spawn", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			f := admissionSetup(t, 1)
			if failure == "spawn" {
				off := false
				_, err := f.svc.StartSession(StartSessionIn{MissionID: f.mission.ID, AccountID: f.account.ID, Argv: []string{"/missing/disposable-cli"}, Inject: &off})
				if err == nil {
					t.Fatal("spawn unexpectedly succeeded")
				}
			} else {
				rejectEvents(t, f.db)
				done := make(chan domain.Session, 1)
				onExit := f.svc.Session.OnExit
				f.svc.Session.OnExit = func(s domain.Session) { onExit(s); done <- s }
				_, err := f.start()
				if err == nil || !strings.Contains(err.Error(), "event rejected") {
					t.Fatalf("persistence: %v", err)
				}
				exited := waitExit(t, done)
				if exited.Process != "exited" || exited.EndedAt == nil {
					t.Fatal(exited)
				}
				sqlExec(t, f.db, "DROP TRIGGER reject_event")
			}
			live, err := f.start()
			if err != nil {
				t.Fatalf("admission leaked after %s: %v", failure, err)
			}
			if _, err := f.svc.StopSession(live.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIndependentAccountAdmission(t *testing.T) {
	f := admissionSetup(t, 1)
	first, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	defer f.svc.StopSession(first.ID)
	if _, err = f.start(); err == nil {
		t.Fatal("capacity accepted")
	}
	a := f.account
	a.ID = "independent"
	a.Label = "b"
	if err := f.svc.Store.PutAccount(a); err != nil {
		t.Fatal(err)
	}
	f.account = a
	second, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	defer f.svc.StopSession(second.ID)
	t.Log(fmt.Sprintf("independent PIDs: %d %d", first.PID, second.PID))
}

func TestConcurrentLoginInitReuse(t *testing.T) {
	for _, mode := range []string{"login", "init"} {
		t.Run(mode, func(t *testing.T) {
			f := admissionSetup(t, 1)
			gate := make(chan struct{})
			type result struct {
				s   domain.Session
				err error
			}
			out := make(chan result, 10)
			for range 10 {
				go func() {
					<-gate
					var s domain.Session
					var err error
					if mode == "login" {
						s, err = f.svc.StartLogin(f.account.ID, "web", "test")
					} else {
						s, err = f.svc.StartInit(f.workspace.ID, "brief", "web", "test")
					}
					out <- result{s, err}
				}()
			}
			close(gate)
			ids := map[string]domain.Session{}
			for range 10 {
				r := <-out
				if r.err != nil {
					t.Error(r.err)
				} else {
					ids[r.s.ID] = r.s
				}
			}
			defer func() {
				for id := range ids {
					f.svc.StopSession(id)
				}
			}()
			if len(ids) != 1 {
				t.Fatalf("reuse spawned %d processes", len(ids))
			}
			for _, s := range ids {
				readyProcess(t, f.svc, s)
			}
			data, err := os.ReadFile(f.launches)
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Fields(string(data))) != 1 {
				t.Fatalf("duplicate launch evidence: %q", data)
			}
			t.Logf("%s: 10 requests reused exactly 1 process", mode)
			if mode == "login" {
				if extra, err := f.start(); err == nil {
					f.svc.StopSession(extra.ID)
					t.Fatal("login did not consume capacity")
				}
			}
		})
	}
}

func TestStartFailuresRecoverAllPaths(t *testing.T) {
	for _, mode := range []string{"session", "login", "init"} {
		for _, failure := range []string{"spawn", "persistence"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				f := admissionSetup(t, 1)
				cli := filepath.Join(filepath.Dir(f.launches), "codex")
				original, err := os.ReadFile(cli)
				if err != nil {
					t.Fatal(err)
				}
				start := func() (domain.Session, error) {
					switch mode {
					case "login":
						return f.svc.StartLogin(f.account.ID, "web", "test")
					case "init":
						return f.svc.StartInit(f.workspace.ID, "brief", "web", "test")
					default:
						return f.start()
					}
				}
				done := make(chan domain.Session, 4)
				onExit := f.svc.Session.OnExit
				f.svc.Session.OnExit = func(s domain.Session) { onExit(s); done <- s }
				if failure == "spawn" {
					if err := os.WriteFile(cli, []byte("#!/missing/interpreter\n"), 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					rejectEvents(t, f.db)
				}
				failed, err := start()
				if err == nil {
					f.svc.StopSession(failed.ID)
					t.Fatal("synthetic failure succeeded")
				}
				if failure == "persistence" {
					if !strings.Contains(err.Error(), "event rejected") {
						t.Fatalf("lost original error: %v", err)
					}
					exited := waitExit(t, done)
					if exited.ID != failed.ID || exited.Process != "exited" || exited.EndedAt == nil {
						t.Fatalf("cleanup: %+v", exited)
					}
					if _, ok := f.svc.Session.Snapshot(failed.ID); ok {
						t.Fatal("failed process still live")
					}
					sqlExec(t, f.db, "DROP TRIGGER reject_event")
				} else {
					if err := os.WriteFile(cli, original, 0755); err != nil {
						t.Fatal(err)
					}
				}
				live, err := start()
				if err != nil {
					t.Fatalf("retry after %s failed: %v", failure, err)
				}
				if _, err := f.svc.StopSession(live.ID); err != nil {
					t.Fatal(err)
				}
				exited := waitExit(t, done)
				if exited.ID != live.ID || exited.EndedAt == nil {
					t.Fatal(exited)
				}
			})
		}
	}
}

func TestUnlimitedAndCurrentAccountCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			f := admissionSetup(t, capacity)
			var live []domain.Session
			defer func() {
				for _, s := range live {
					f.svc.StopSession(s.ID)
				}
			}()
			for range 4 {
				s, err := f.start()
				if err != nil {
					t.Fatal(err)
				}
				live = append(live, s)
			}
			f.account.SessionCap = 1
			if err := f.svc.Store.PutAccount(f.account); err != nil {
				t.Fatal(err)
			}
			if s, err := f.start(); err == nil {
				live = append(live, s)
				t.Fatal("new positive capacity ignored")
			}
			for _, s := range live {
				if _, err := f.svc.StopSession(s.ID); err != nil {
					t.Fatal(err)
				}
			}
			live = nil
			s, err := f.start()
			if err != nil {
				t.Fatal(err)
			}
			live = append(live, s)
		})
	}
}

func TestCatalogRowsAreNotAdmissions(t *testing.T) {
	f := admissionSetup(t, 1)
	if err := f.svc.Store.PutSession(domain.Session{ID: "old-catalog-only", AccountID: f.account.ID, Process: "running"}); err != nil {
		t.Fatal(err)
	}
	live, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.StopSession(live.ID); err != nil {
		t.Fatal(err)
	}
}
