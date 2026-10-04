package service

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestFreshStartOwnsPTYWithoutTake(t *testing.T) {
	for _, mode := range []string{"session", "login", "init"} {
		for _, owner := range []string{"web", "tui"} {
			t.Run(mode+"/"+owner, func(t *testing.T) {
				f := admissionSetup(t, 1)
				client := "requester-" + owner
				var live domain.Session
				var err error
				switch mode {
				case "login":
					live, err = f.svc.StartLogin(f.account.ID, owner, client)
				case "init":
					live, err = f.svc.StartInit(f.workspace.ID, "brief", owner, client, confirmedFixtureInput(f.account.ID))
				default:
					off := false
					live, err = f.svc.StartSession(StartSessionIn{MissionID: f.mission.ID, AccountID: f.account.ID, Inject: &off, Owner: owner, Client: client})
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := f.svc.StopSession(live.ID); err != nil {
						t.Error(err)
					}
				}()
				stored, err := f.svc.Store.GetSession(live.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range []domain.Session{live, stored} {
					if s.Owner != owner || s.OwnerClient != client {
						t.Fatalf("fresh ownership: %+v; want %s/%s", s, owner, client)
					}
					wantAccount := f.account.ID
					if s.AccountID != wantAccount {
						t.Fatalf("account binding: %q; want %q", s.AccountID, wantAccount)
					}
				}

				acknowledged := make(chan struct{}, 1)
				var mu sync.Mutex
				var output strings.Builder
				unsub, err := f.svc.Session.Subscribe(live.ID, func(b []byte) {
					mu.Lock()
					defer mu.Unlock()
					output.Write(b)
					if strings.Contains(output.String(), "ack:owned-by-"+client) {
						select {
						case acknowledged <- struct{}{}:
						default:
						}
					}
				})
				if err != nil {
					t.Fatal(err)
				}
				defer unsub()
				// Neither this test nor its setup calls Take: constructor ownership
				// must already authorize input on the real process's PTY.
				if err := f.svc.Session.Input(live.ID, "another-client", []byte("unauthorized\n")); !errors.Is(err, os.ErrPermission) {
					t.Fatalf("other client input: %v; want permission denied", err)
				}
				if err := f.svc.Session.Input(live.ID, client, []byte("owned-by-"+client+"\n")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-acknowledged:
				case <-time.After(5 * time.Second):
					t.Fatal("process did not acknowledge requester input without Take")
				}
				data, err := os.ReadFile(f.launches)
				if err != nil {
					t.Fatal(err)
				}
				if count := len(strings.Fields(string(data))); count != 1 {
					t.Fatalf("fresh start launched %d processes; want 1", count)
				}
				t.Logf("%s/%s: one real process acknowledged requester input without Take; other client refused; AccountID=%q", mode, owner, live.AccountID)
			})
		}
	}
}
