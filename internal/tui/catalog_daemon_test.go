package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/testfixture"
)

func TestCatalogRealDaemonSaveReadinessAndCanonicalSnapshots(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			st, err := store.Open(filepath.Join(root, "catalog.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			svc := service.New(st, nil, nil)
			svc.Home = root
			// Lookup only. Readiness is injected; this file is never executed.
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, testfixture.ExecutableName(provider)), nil, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			var probes atomic.Int32
			svc.Readiness = adapters.NewReadinessChecker(func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
				probes.Add(1)
				if len(argv) != 2 || argv[1] != "--version" || !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
					t.Errorf("non-isolated/non-diagnostic probe: %v %s", argv, dir)
				}
				return []byte("unqualified fixture version"), 0, nil
			}, nil)
			a := domain.Account{ID: "exact-" + provider, Adapter: provider, Isolation: domain.IsolationSupported, Connected: true}
			ad, err := adapters.Get(provider)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ad.IsolateEnv(root, a); err != nil {
				t.Fatal(err)
			}
			if err = st.PutAccount(a); err != nil {
				t.Fatal(err)
			}
			ws, err := svc.CreateWorkspace("fixture", root)
			if err != nil {
				t.Fatal(err)
			}
			handler := (&api.Server{Svc: svc, Token: "disposable-account-fixture"}).Handler()
			var writes []string
			c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					writes = append(writes, r.URL.Path)
				}
				handler.ServeHTTP(w, r)
			})
			m := New(c).(model)
			m.accounts = []domain.Account{a}
			m.ws = []domain.Workspace{ws}
			m = catalogDraft(t, m, provider)
			m, cmd := catalogKey(m, "ctrl+v")
			m = catalogFinish(t, m, cmd)
			if m.catalog.validation == nil || m.catalog.validation.Ready || !strings.Contains(m.catalog.validation.Title, "cli_incompatible") || probes.Load() != 1 || m.editor.ID != "" {
				t.Fatalf("validation: %+v probes=%d", m.catalog.validation, probes.Load())
			}
			for range 2 {
				m, cmd = catalogKey(m, "ctrl+s")
				m = catalogFinish(t, m, cmd)
			}
			saved := cloneSquad(m.editor)
			if saved.ID == "" || saved.Version != 2 || saved.Origin != "custom" || saved.Draft || saved.Members[0].ID == "" || probes.Load() != 1 {
				t.Fatalf("structural save vs readiness %+v probes=%d", saved, probes.Load())
			}
			rows, err := st.ListSquads()
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows %v %v", rows, err)
			}
			if !reflect.DeepEqual(rows[0], saved) {
				t.Fatal("save reply not actual store identity")
			}
			m, _ = catalogKey(m, "esc")
			m, _ = catalogKey(m, "m")
			m, _ = catalogKey(m, "n")
			m = catalogPaste(m, "User objective")
			m = catalogTabs(m, 1)
			m = catalogPaste(m, "multi\nline")
			m = catalogTabs(m, 1)
			m, _ = catalogKey(m, "right")
			m = catalogTabs(m, 1)
			m, _ = catalogKey(m, "right")
			m, cmd = catalogKey(m, "ctrl+enter")
			if cmd != nil {
				t.Fatal("review wrote catalog")
			}
			changed := cloneSquad(saved)
			changed.Members[0].Model = "server-current-model"
			changed, err = svc.SaveSquad(changed)
			if err != nil {
				t.Fatal(err)
			}
			m, cmd = catalogKey(m, "ctrl+enter")
			m = catalogFinish(t, m, cmd)
			actual, err := st.GetMission(m.missionID)
			if err != nil {
				t.Fatal(err)
			}
			if actual.WorkspaceID != ws.ID || actual.SquadID != saved.ID || actual.SquadSnapshot.Version != 3 || actual.SquadSnapshot.Members[0].Model != "server-current-model" || actual.Binding != nil || actual.Status != "open" || !reflect.DeepEqual(actual, m.catalog.selected) {
				t.Fatalf("canonical snapshot %+v", actual)
			}
			accountText(t, m, "Server snapshot differs")
			accountText(t, m, "v3")
			accountText(t, m, saved.Members[0].ID)
			if m.sessID != "" || m.link != nil || m.termMode || probes.Load() != 1 {
				t.Fatal("catalog mission ran/attached/probed")
			}
			if !reflect.DeepEqual(writes, []string{"/v0/squads/draft/validate", "/v0/squads", "/v0/squads", "/v0/missions"}) {
				t.Fatalf("unexpected writes: %v", writes)
			}
			missions, err := st.ListMissions(ws.ID)
			if err != nil || len(missions) != 1 {
				t.Fatalf("missions %v %v", missions, err)
			}

			secondAccount := domain.Account{ID: "second-" + provider, Adapter: provider, Isolation: domain.IsolationSupported}
			if err := st.PutAccount(secondAccount); err != nil {
				t.Fatal(err)
			}
			secondSquad, err := svc.SaveSquad(domain.Squad{Name: "Other actual squad", Members: []domain.Member{{Role: "Other lead", CLI: provider, AccountID: secondAccount.ID, Model: "other-model", Maestro: true}}})
			if err != nil {
				t.Fatal(err)
			}
			m = catalogFinish(t, m, m.readCatalog())
			m, _ = catalogKey(m, "m")
			m, _ = catalogKey(m, "n")
			m = catalogPaste(m, "Second real objective")
			m = catalogTabs(m, 3)
			for range len(m.squads) + 1 {
				m, _ = catalogKey(m, "right")
				if m.catalog.mission.squad.ID == secondSquad.ID {
					break
				}
			}
			if m.catalog.mission.squad.ID != secondSquad.ID {
				t.Fatal("could not explicitly select second real squad")
			}
			m, _ = catalogKey(m, "ctrl+enter")
			m, cmd = catalogKey(m, "ctrl+enter")
			m = catalogFinish(t, m, cmd)
			second, err := st.GetMission(m.missionID)
			if err != nil {
				t.Fatal(err)
			}
			if second.ID == actual.ID || second.SquadSnapshot.Members[0].ID == actual.SquadSnapshot.Members[0].ID || second.SquadSnapshot.Members[0].AccountID != secondAccount.ID {
				t.Fatal("second actual mission/snapshot identities reused")
			}
			for _, target := range []domain.Mission{actual, second} {
				m, _ = catalogKey(m, "m")
				for i, candidate := range m.missions {
					if candidate.ID == target.ID {
						for range i {
							m, _ = catalogKey(m, "down")
						}
						break
					}
				}
				m, cmd = catalogKey(m, "enter")
				m = catalogFinish(t, m, cmd)
				if !reflect.DeepEqual(m.catalog.selected, target) || m.workspace.selected.ID != target.WorkspaceID {
					t.Fatal("selection did not retain actual immutable mission snapshot")
				}
				accountText(t, m, target.ID)
				accountText(t, m, target.SquadSnapshot.Members[0].ID)
			}
			if len(writes) != 5 || probes.Load() != 1 {
				t.Fatalf("second create caused unexpected writes/probes: %v %d", writes, probes.Load())
			}
		})
	}
}

func TestCatalogValidationFailuresPreserveAllFields(t *testing.T) {
	t.Setenv("BATUTA_LANG", "en")
	for _, provider := range []string{"codex", "claude"} {
		for _, reason := range []string{"unknown account", "account adapter mismatch", "cli_missing", "authentication_required", "cli_incompatible"} {
			t.Run(provider+"/"+reason, func(t *testing.T) {
				var calls int
				c := accountFixture(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method != "POST" || r.URL.Path != "/v0/squads/existing/validate" {
						t.Errorf("unexpected request %s", r.URL.Path)
					}
					var q domain.Squad
					_ = json.NewDecoder(r.Body).Decode(&q)
					_ = json.NewEncoder(w).Encode(map[string]any{"squad": q, "validation": domain.Validation{Title: "Validation: " + reason, Ready: false}})
				})
				m := New(c).(model)
				m = m.startSquad(domain.Squad{ID: "existing", Version: 7, Origin: "batuta", Name: "Preserve", Purpose: "all fields", Members: []domain.Member{{ID: "member", CLI: provider, AccountID: "explicit", Model: "", Effort: "", Skills: []string{"one", "two"}, Maestro: true}}})
				before := cloneSquad(m.editor)
				m, cmd := catalogKey(m, "ctrl+v")
				m = catalogFinish(t, m, cmd)
				if !reflect.DeepEqual(m.editor, before) || calls != 1 {
					t.Fatal("failed validation changed draft")
				}
				accountText(t, m, reason)
				accountText(t, m, "empty / inherited")
			})
		}
	}
}
