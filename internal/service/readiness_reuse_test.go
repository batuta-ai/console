package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestReadinessIsRecheckedBeforeReuse(t *testing.T) {
	for _, mode := range []string{"login", "init"} {
		t.Run(mode, func(t *testing.T) {
			f := admissionSetup(t, 3)
			start := func(client string) (domain.Session, error) {
				if mode == "login" {
					return f.svc.StartLogin(f.account.ID, "web", client)
				}
				return f.svc.StartInit(f.workspace.ID, "synthetic", "web", client, confirmedFixtureInput(f.account.ID))
			}
			live, err := start("original")
			if err != nil {
				t.Fatal(err)
			}
			defer f.svc.StopSession(live.ID)
			readyProcess(t, f.svc, live)
			stored, err := f.svc.Store.GetSession(live.ID)
			if err != nil {
				t.Fatal(err)
			}
			seq, err := f.svc.Store.LastEventSeq()
			if err != nil {
				t.Fatal(err)
			}
			want := "credential_missing"
			if mode == "login" {
				want = "cli_incompatible"
				if err := testfixture.UpdateScriptInPlace(t, filepath.Join(filepath.Dir(f.launches), testfixture.ExecutableName("codex")), []byte("#!/bin/sh\necho 'codex-cli 99.0.0'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(filepath.Join(f.svc.Home, "accounts", f.account.ID, ".codex", "auth.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := start("new-client"); err == nil {
				t.Fatal("unready account reused an existing process")
			} else if code, ok := ReadinessFailure(err); !ok || code != want {
				t.Fatalf("wrong recovery diagnostic: %v", err)
			}
			after, err := f.svc.Store.GetSession(live.ID)
			if err != nil || after.OwnerClient != stored.OwnerClient {
				t.Fatalf("reuse changed ownership: %+v %v", after, err)
			}
			events, err := f.svc.Store.EventsSince(seq, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("rejected reuse emitted events: %v %v", events, err)
			}
		})
	}
}

func TestSlowReadinessDoesNotBlockUnrelatedCanceledRequest(t *testing.T) {
	for _, mode := range []string{"login", "init"} {
		t.Run(mode, func(t *testing.T) {
			f := admissionSetup(t, 3)
			b, err := f.svc.CreateAccount("z-second", "codex")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.ConnectAccount(b.ID); err != nil {
				t.Fatal(err)
			}
			w, err := f.svc.CreateWorkspace("second", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Dir(f.launches)
			marker, release := filepath.Join(dir, "checking"), filepath.Join(dir, "release")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf started > '" + marker + "'; while [ ! -e '" + release + "' ]; do /bin/sleep 0.01; done; echo 'codex-cli 0.160.0'; exit 0; fi\nwhile IFS= read -r line; do printf 'ack:%s\\n' \"$line\"; done\n"
			if err := testfixture.RewriteScript(t, filepath.Join(dir, testfixture.ExecutableName("codex")), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			type result struct {
				live domain.Session
				err  error
			}
			first := make(chan result, 1)
			go func() {
				var live domain.Session
				var err error
				if mode == "login" {
					live, err = f.svc.StartLogin(f.account.ID, "web", "first")
				} else {
					live, err = f.svc.StartInit(f.workspace.ID, "synthetic", "web", "first", confirmedFixtureInput(f.account.ID))
				}
				first <- result{live, err}
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("first native probe did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			second := make(chan result, 1)
			go func() {
				var live domain.Session
				var err error
				if mode == "login" {
					live, err = f.svc.StartLoginContext(ctx, b.ID, "web", "second")
				} else {
					live, err = f.svc.StartInitContext(ctx, w.ID, "synthetic", "web", "second", confirmedFixtureInput(b.ID))
				}
				second <- result{live, err}
			}()
			var canceled result
			blocked := false
			select {
			case canceled = <-second:
			case <-time.After(500 * time.Millisecond):
				blocked = true
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			original := <-first
			if original.live.PID != 0 {
				defer f.svc.StopSession(original.live.ID)
			}
			if original.err != nil {
				t.Fatal(original.err)
			}
			if blocked {
				canceled = <-second
				t.Error("unrelated request waited for another account's readiness despite cancellation")
			}
			if canceled.live.PID != 0 {
				f.svc.StopSession(canceled.live.ID)
				t.Error("canceled request launched a process")
			}
			if code, ok := ReadinessFailure(canceled.err); !ok || code != "check_cancelled" {
				t.Fatalf("cancellation diagnosis: %v", canceled.err)
			}
		})
	}
}

func TestInitSharesSelectedAccountCapacity(t *testing.T) {
	f := admissionSetup(t, 1)
	live, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	defer f.svc.StopSession(live.ID)
	readyProcess(t, f.svc, live)
	if extra, err := f.svc.StartInit(f.workspace.ID, "synthetic", "web", "init", confirmedFixtureInput(f.account.ID)); err == nil {
		f.svc.StopSession(extra.ID)
		t.Fatal("init bypassed selected account capacity")
	} else if !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("init did not reach capacity admission: %v", err)
	}
	rows, err := f.svc.Store.ListSessions()
	if err != nil || len(rows) != 1 || rows[0].AccountID != f.account.ID {
		t.Fatalf("init capacity rejection mutated session catalog: %v %v", rows, err)
	}
}

func TestInitSkipsUnusableUnselectedAccount(t *testing.T) {
	f := admissionSetup(t, 3)
	other, err := f.svc.CreateAccount("0-unavailable", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ConnectAccount(other.ID); err != nil {
		t.Fatal(err)
	}
	live, err := f.svc.StartInit(f.workspace.ID, "synthetic", "web", "test", confirmedFixtureInput(f.account.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer f.svc.StopSession(live.ID)
	readyProcess(t, f.svc, live)
	if live.AccountID != f.account.ID {
		t.Fatalf("init did not select the ready catalog identity: %+v", live)
	}
}

func TestInitKeepsExistingIdentityWhenAccountOrderChanges(t *testing.T) {
	f := admissionSetup(t, 3)
	other, err := f.svc.CreateAccount("z-later", "codex")
	if err != nil {
		t.Fatal(err)
	}
	other, err = f.svc.ConnectAccount(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	authenticateFixtureAccount(t, f.svc, other, "B")
	live, err := f.svc.StartInit(f.workspace.ID, "synthetic", "web", "original", confirmedFixtureInput(f.account.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer f.svc.StopSession(live.ID)
	readyProcess(t, f.svc, live)
	if _, err := f.svc.RenameAccount(other.ID, "0-first"); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.StartInit(f.workspace.ID, "synthetic", "web", "new", confirmedFixtureInput(f.account.ID))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != live.ID {
		f.svc.StopSession(again.ID)
		t.Fatalf("account ordering started a second init: first=%+v second=%+v", live, again)
	}
	if again.AccountID != f.account.ID {
		t.Fatalf("init switched identity: %+v", again)
	}
}
