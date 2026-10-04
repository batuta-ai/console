package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/i18n"
	"github.com/batuta-ai/console/internal/store"
)

func TestCapabilitiesObservedHTTP(t *testing.T) { testCapabilitiesObserved(t, true) }

func TestCapabilitiesObservedHandler(t *testing.T) { testCapabilitiesObserved(t, false) }

func testCapabilitiesObserved(t *testing.T, network bool) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	for _, id := range adapters.IDs() {
		if id == "claude" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, id), []byte("#!/bin/sh\n: > '"+marker+"'\nexit 91\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	h, svc := testServer(t)
	account := domain.Account{ID: "acct_capability", Label: "Isolated fixture", Adapter: "codex", Connected: true, Authed: true, Provider: "openai", AuthMethod: "fixture", VaultRef: "owned-fixture", SessionCap: 3, Isolation: domain.IsolationSupported}
	if err := svc.Store.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: "ws_capability", Name: "Owned workspace", Root: dir}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutSquad(domain.Squad{ID: "sq_capability", Name: "Owned squad", Version: 2, Members: []domain.Member{{ID: "member_capability", CLI: "codex", AccountID: account.ID}}}); err != nil {
		t.Fatal(err)
	}
	beforeCatalog := snapshotCapabilityCatalog(t, svc.Store)
	if err := os.WriteFile(filepath.Join(svc.Home, "identity-sentinel"), []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(svc.Home, "owned-identity")
	if err := os.MkdirAll(filepath.Join(owned, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owned, "nested", "credentials-fixture"), []byte("owned synthetic identity"), 0600); err != nil {
		t.Fatal(err)
	}
	beforeOwned := snapshotCapabilityIdentity(t, owned)
	beforeFiles, err := os.ReadDir(svc.Home)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Store.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	var resp *http.Response
	if network {
		ts := httptest.NewServer(h)
		defer ts.Close()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v0/capabilities", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err = ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		req := httptest.NewRequest(http.MethodGet, "/v0/capabilities", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		resp = w.Result()
	}
	defer resp.Body.Close()
	var got map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"api": "v0", "product": "batuta-console", "lang": i18n.Lang(), "integrations": []string{"generic", "batuta"}} {
		expected, err := json.Marshal(want)
		if err != nil || string(got[key]) != string(expected) {
			t.Fatalf("legacy %s: %s, want %s", key, got[key], expected)
		}
	}
	var host struct {
		OS, Arch  string
		GoVersion string `json:"go_version"`
		Selected  bool
	}
	if err := json.Unmarshal(got["runtime"], &host); err != nil {
		t.Fatalf("runtime capability missing: %v", err)
	}
	var build struct {
		Version, Revision string
		Modified          *bool
	}
	var runtimeRaw struct{ Build json.RawMessage }
	_ = json.Unmarshal(got["runtime"], &runtimeRaw)
	if err := json.Unmarshal(runtimeRaw.Build, &build); err != nil || build.Version == "" || build.Revision == "" {
		t.Fatalf("build %s", got["runtime"])
	}
	var matrix struct {
		Platforms, Providers []string
		Qualification        struct{ Status, Code string }
	}
	if err := json.Unmarshal(got["matrix"], &matrix); err != nil || !reflect.DeepEqual(matrix.Platforms, []string{"darwin", "linux"}) || !reflect.DeepEqual(matrix.Providers, []string{"codex", "claude"}) || matrix.Qualification.Status != "pending" {
		t.Fatalf("matrix %s", got["matrix"])
	}
	if host.OS != runtime.GOOS || host.Arch != runtime.GOARCH || host.GoVersion != runtime.Version() || !host.Selected {
		t.Fatalf("runtime %+v", host)
	}
	var providers []struct {
		ID                  string
		Selected, Supported bool
		Installation        struct{ Status, Code string }
		Compatibility       struct{ Status, Code string }
		Qualification       struct{ Status, Code string }
		AccountReadiness    struct{ Status, Code string } `json:"account_readiness"`
	}
	if err := json.Unmarshal(got["providers"], &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers) != len(adapters.IDs()) {
		t.Fatalf("providers %s", got["providers"])
	}
	for _, p := range providers {
		selected := p.ID == "codex" || p.ID == "claude"
		if p.Selected != selected || p.Supported != selected {
			t.Fatalf("matrix %+v", p)
		}
		status := "present"
		if p.ID == "claude" {
			status = "missing"
		}
		if p.Installation.Status != status {
			t.Fatalf("installation %+v", p)
		}
		compatibility := "not_checked"
		if !selected {
			compatibility = "unsupported"
		}
		if p.Compatibility.Status != compatibility || p.AccountReadiness.Status != "not_checked" {
			t.Fatalf("unchecked %+v", p)
		}
		qualification := "pending"
		if !selected {
			qualification = "unsupported"
		}
		if p.Qualification.Status != qualification {
			t.Fatalf("qualification %+v", p)
		}
	}
	var ids []string
	if err := json.Unmarshal(got["adapters"], &ids); err != nil || !reflect.DeepEqual(ids, adapters.IDs()) {
		t.Fatalf("legacy adapters %s", got["adapters"])
	}
	var pt struct {
		Supported, Available bool
		Code                 string
		Qualification        struct{ Status string }
	}
	if err := json.Unmarshal(got["pty"], &pt); err != nil {
		t.Fatal(err)
	}
	var features map[string]bool
	if err := json.Unmarshal(got["features"], &features); err != nil {
		t.Fatal(err)
	}
	wantFeatures := map[string]bool{"pty": pt.Available, "watch": true, "limits": true, "batuta_inject": true, "batuta_inject_default": true, "skills_catalog": true, "account_fallback": false, "folder_picker": true}
	if !reflect.DeepEqual(features, wantFeatures) {
		t.Fatalf("legacy features: %#v", features)
	}
	if !pt.Supported || !pt.Available || pt.Code != "pty_available" || features["pty"] != pt.Available || pt.Qualification.Status != "pending" {
		t.Fatalf("pty %s", got["pty"])
	}
	if afterCatalog := snapshotCapabilityCatalog(t, svc.Store); !reflect.DeepEqual(beforeCatalog, afterCatalog) {
		t.Fatal("catalog identity changed")
	}
	if afterOwned := snapshotCapabilityIdentity(t, owned); !reflect.DeepEqual(beforeOwned, afterOwned) {
		t.Fatal("nested identity contents/modes changed")
	}
	after, err := svc.Store.ListAccounts()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("account mutation %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("capabilities executed provider")
	}
	afterFiles, err := os.ReadDir(svc.Home)
	if err != nil || len(afterFiles) != len(beforeFiles) {
		t.Fatal("filesystem mutation")
	}
	for i := range beforeFiles {
		if beforeFiles[i].Name() != afterFiles[i].Name() {
			t.Fatal("filesystem mutation")
		}
	}
	sentinel, err := os.ReadFile(filepath.Join(svc.Home, "identity-sentinel"))
	if err != nil || string(sentinel) != "unchanged" {
		t.Fatal("identity mutation")
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), dir) {
		t.Fatal("private path leaked")
	}
}

func TestCapabilitiesCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v0/capabilities", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	(&Server{Token: "test-token"}).Handler().ServeHTTP(w, req)
	var got struct {
		PTY struct {
			Available bool
			Code      string
		}
		Features map[string]bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PTY.Available || got.Features["pty"] || got.PTY.Code != "check_cancelled" {
		t.Fatalf("cancelled %s", w.Body.String())
	}
}

// Only the owned identity tree is snapshotted; SQLite's incidental files are not credential identity.
func snapshotCapabilityIdentity(t *testing.T, root string) map[string]struct {
	Mode fs.FileMode
	Data string
} {
	t.Helper()
	result := make(map[string]struct {
		Mode fs.FileMode
		Data string
	})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[rel] = struct {
			Mode fs.FileMode
			Data string
		}{info.Mode(), string(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCapabilitiesCancellationHTTP(t *testing.T) {
	entered := make(chan struct{})
	observed := make(chan capabilityResponse, 1)
	handler := (&Server{Token: "test-token"}).Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		var got capabilityResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Error(err)
		}
		observed <- got
	}))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v0/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	completed := make(chan error, 1)
	go func() {
		resp, err := ts.Client().Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		completed <- err
	}()
	select {
	case <-entered:
	case err := <-completed:
		t.Fatalf("request did not reach server: %v", err)
	}
	cancel()
	if err := <-completed; err == nil {
		t.Fatal("cancelled transport succeeded")
	}
	got := <-observed
	if got.PTY.Available || got.Features["pty"] || got.PTY.Code != "check_cancelled" {
		t.Fatalf("server cancellation: %+v", got.PTY)
	}
}

func snapshotCapabilityCatalog(t *testing.T, st *store.Store) []byte {
	t.Helper()
	workspaces, err := st.ListWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	squads, err := st.ListSquads()
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := st.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := st.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	missions, err := st.ListMissions("ws_capability")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Workspaces []domain.Workspace
		Squads     []domain.Squad
		Accounts   []domain.Account
		Sessions   []domain.Session
		Missions   []domain.Mission
	}{workspaces, squads, accounts, sessions, missions})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
