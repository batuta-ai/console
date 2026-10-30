package session_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	secure "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"golang.org/x/sys/windows"
)

// These bytes are shared by the old-policy RED and corrected-policy GREEN.
// Contract: bdee6d66c97855ca50cdadb4efe03459d0b08674596ba09650ce8ac6d3e9007e
// Plan: 171c802535825cea1f48e040c2e2498049c9dd5565f8cfb3ce437e0a86afe7fb
// The only copied executable is a synthetic provider. Core always runs in the
// real consoled image built from this checkout; no protocol frames are relayed.
func TestMain(m *testing.M) {
	if strings.EqualFold(filepath.Base(os.Args[0]), "codex.exe") {
		if err := contractProvider(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type contractBirth struct {
	PID      uint32
	Creation uint64
}
type contractBirths struct {
	Provider, Core contractBirth
	Root           string
}

func contractBirthOf(handle windows.Handle) (contractBirth, error) {
	pid, err := windows.GetProcessId(handle)
	if err != nil {
		return contractBirth{}, err
	}
	var born, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &born, &exited, &kernel, &user); err != nil {
		return contractBirth{}, err
	}
	return contractBirth{pid, uint64(born.HighDateTime)<<32 | uint64(born.LowDateTime)}, nil
}

func contractCoreBirth() (contractBirth, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return contractBirth{}, err
	}
	defer windows.CloseHandle(snapshot)
	entries := map[uint32]windows.ProcessEntry32{}
	var entry windows.ProcessEntry32
	// Sizeof is only for the Windows ABI; no private Go object crosses packages.
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		entries[entry.ProcessID] = entry
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return contractBirth{}, err
	}
	pid := uint32(os.Getpid())
	for depth := 0; depth < 32; depth++ {
		entry, ok := entries[pid]
		if !ok {
			break
		}
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "consoled.exe") {
			// This birth witness grants no termination rights. The parent bridge must
			// independently match it against an already-owned original process handle.
			handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil {
				return contractBirth{}, err
			}
			birth, observeErr := contractBirthOf(handle)
			return birth, errors.Join(observeErr, windows.CloseHandle(handle))
		}
		pid = entry.ParentProcessID
	}
	return contractBirth{}, errors.New("real consoled ancestor missing")
}

func contractEventName(common string) string {
	return fmt.Sprintf("Local\\BatutaCoreContract-%x", sha256.Sum256([]byte(strings.ToLower(filepath.Clean(common)))))
}

func contractProvider() (resultErr error) {
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return errors.New("ambient credential crossed provider boundary")
	}
	switch strings.Join(os.Args[1:], " ") {
	case "--version", "version":
		fmt.Println("codex-cli 0.160.0")
		return nil
	case "debug models", "debug models --bundled", "models":
		fmt.Println(`{"models":[{"slug":"qa-native"}]}`)
		return nil
	case "doctor --json --summary":
		fmt.Println(`{"schema_version":1}`)
		return nil
	case "plugin list --json", "plugin marketplace list --json":
		fmt.Println(`{}`)
		return nil
	}
	if len(os.Args) != 5 || (os.Args[1] != "run" && os.Args[1] != "review") || os.Args[2] != "--model" || os.Args[3] != "qa-native" {
		return fmt.Errorf("unexpected synthetic provider argv: %q", os.Args[1:])
	}
	if os.Args[1] == "review" {
		fmt.Println("<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE")
		return nil
	}
	if strings.Contains(os.Args[4], "BATUTA-CAPABLE") {
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return err
		}
		fmt.Printf("BATUTA-CAPABLE %s\n", strings.TrimSpace(string(out)))
		return nil
	}
	out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return err
	}
	common := strings.TrimSpace(string(out))
	name, err := windows.UTF16PtrFromString(contractEventName(common))
	if err != nil {
		return err
	}
	gate, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(gate)
	provider, err := contractBirthOf(windows.CurrentProcess())
	if err != nil {
		return err
	}
	core, err := contractCoreBirth()
	if err != nil {
		return err
	}
	data, err := json.Marshal(contractBirths{Provider: provider, Core: core, Root: filepath.Dir(common)})
	if err != nil {
		return err
	}
	path := filepath.Join(common, "core-contract-birth.json")
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	readyName, err := windows.UTF16PtrFromString(contractEventName(common) + "-birth-ready")
	if err != nil {
		return err
	}
	ready, err := windows.CreateEvent(nil, 1, 0, readyName)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, windows.CloseHandle(ready))
	}()
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	if err := windows.SetEvent(ready); err != nil {
		return err
	}
	status, err := windows.WaitForSingleObject(gate, windows.INFINITE)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("provider release wait=%d: %w", status, err)
	}
	fmt.Println("BATUTA-QUESTION: which approved output?")
	return nil
}

func TestNativeCoreServiceContractBirthPublicationWaitsForReady(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "core-contract-birth.json")
	readyName := contractEventName(root) + "-birth-ready"
	name, err := windows.UTF16PtrFromString(readyName)
	if err != nil {
		t.Fatal(err)
	}
	event, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(event); err != nil {
			t.Error(err)
		}
	})
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := windows.CreateFile(ptr, windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if writer != 0 {
			if err := windows.CloseHandle(writer); err != nil {
				t.Error(err)
			}
		}
	})
	want := []byte("{\"Provider\":{\"PID\":11,\"Creation\":22},\"Core\":{\"PID\":33,\"Creation\":44},\"Root\":\"publication\"}\n")
	var written uint32
	if err := windows.WriteFile(writer, want, &written, nil); err != nil {
		t.Fatal(err)
	}
	if written != uint32(len(want)) {
		t.Fatal("publication bytes incomplete", written)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("marker must be visible before publication completes", err)
	}
	if got, ready, err := contractReadBirth(path, readyName); err != nil || ready || len(got) != 0 {
		t.Fatalf("read before completed publication: ready=%t bytes=%q sharingViolation=%t err=%v", ready, got, errors.Is(err, windows.ERROR_SHARING_VIOLATION), err)
	}
	if err := windows.CloseHandle(writer); err != nil {
		t.Fatal(err)
	}
	writer = 0
	// Readability alone is not the publisher's completion acknowledgment.
	if got, ready, err := contractReadBirth(path, readyName); err != nil || ready || len(got) != 0 {
		t.Fatalf("read before acknowledgment: ready=%t bytes=%q err=%v", ready, got, err)
	}
	if err := windows.SetEvent(event); err != nil {
		t.Fatal(err)
	}
	got, ready, err := contractReadBirth(path, readyName)
	if err != nil || !ready || !bytes.Equal(got, want) {
		t.Fatalf("completed publication: ready=%t bytes=%q err=%v", ready, got, err)
	}
	// A completed publication must not hide a subsequent filesystem error.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got, ready, err := contractReadBirth(path, readyName); err == nil || ready || len(got) != 0 {
		t.Fatalf("completed publication read error lost: ready=%t bytes=%q err=%v", ready, got, err)
	}
}

func contractHome(t *testing.T) string {
	t.Helper()
	_, home, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	return home
}

func contractWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := secure.EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	file, err := secure.CreatePrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Write(body)
	if err := errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}
}

func contractImage(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source root unavailable")
	}
	image := filepath.Join(contractHome(t), "consoled.exe")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-race", "-mod=readonly", "-o", image, "./cmd/consoled")
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real daemon build: %v\n%s", err, out)
	}
	metadata, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "version", "-m", image).CombinedOutput()
	if err != nil || !strings.Contains(string(metadata), "-race=true") {
		t.Fatalf("real daemon race metadata: %v\n%s", err, metadata)
	}
	t.Logf("real daemon build metadata:\n%s", metadata)
	return image
}

func contractAuthenticate(t *testing.T, svc *service.Service, account domain.Account) {
	t.Helper()
	ad, err := adapters.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.IsolateEnv(svc.Home, account); err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account.ID}})
	if err != nil {
		t.Fatal(err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic-signature"
	payload, err := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": token, "id_token": token, "refresh_token": "synthetic-refresh-never-real", "account_id": account.ID}})
	if err != nil {
		t.Fatal(err)
	}
	contractWrite(t, filepath.Join(ad.IsolateDir(svc.Home, account.ID), ".codex", "auth.json"), payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Host != "chatgpt.com" || r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("ChatGPT-Account-Id") != account.ID {
			t.Error("unexpected synthetic authentication request")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"user_id": "synthetic-user", "account_id": account.ID, "plan_type": "pro", "rate_limit": map[string]bool{"allowed": true, "limit_reached": false}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "chatgpt.com:443" {
			return nil, fmt.Errorf("unexpected provider destination %s %s", network, address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	probe := func(ctx context.Context, argv, env []string, dir string) ([]byte, int, error) {
		result, err := svc.Session.Probe(ctx, argv, env, dir)
		return result.Output, result.ExitCode, err
	}
	svc.Readiness = adapters.NewReadinessChecker(probe, &http.Client{Transport: transport})
}

type contractFixture struct {
	svc      *service.Service
	approval domain.PlanApproval
	gate     windows.Handle
}

func newContractFixture(t *testing.T, image string) contractFixture {
	t.Helper()
	root, tools, collection, home := contractHome(t), contractHome(t), contractHome(t), contractHome(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	contractWrite(t, filepath.Join(tools, "codex.exe"), data)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "synthetic-must-not-cross")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-must-not-cross")
	for _, name := range []string{"batuta", "batuta-loop", "batuta-review"} {
		contractWrite(t, filepath.Join(collection, name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: synthetic fixture\n---\n"))
	}
	skills := filepath.Join(collection, "batuta")
	t.Setenv("BATUTA_SKILLS", skills)
	contractWrite(t, filepath.Join(skills, "adapters", "codex.md"), []byte(`---
name: codex
executable: codex
run: 'codex run --model {model} "{brief}" < /dev/null'
readonly: 'codex review --model {model} "{prompt}" < /dev/null'
available: 'codex version'
models: 'codex models'
model_flags: --model {model}
finished: exit_code
brief_limit_lines: 2000
cwd_flag: env
---
`))
	contractWrite(t, filepath.Join(skills, "templates", "generic.md"), []byte("# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n"))
	files := map[string]string{
		".batuta/profile.md":     "# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n",
		".batuta/routing.md":     "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | qa-native |\n| medium | * | codex | qa-native |\n| high | * | codex | qa-native |\n| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | codex | qa-native |\n",
		".batuta/plans/alpha.md": "# Plan — Alpha\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n",
		".gitignore":             ".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n",
	}
	for name, body := range files {
		contractWrite(t, filepath.Join(root, filepath.FromSlash(name)), []byte(body))
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "console-native-qa"}, {"config", "user.email", "qa@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "chore: native contract fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("Git fixture: %v\n%s", err, out)
		}
	}
	st, err := store.Open(filepath.Join(home, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	svc := service.New(st, session.New(home), nil)
	svc.Home, svc.NativeExecutorExecutable = home, image
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := svc.ShutdownNative(ctx); err != nil {
			t.Error(err)
		}
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	account := domain.Account{ID: "acct_core_contract", Adapter: "codex", Connected: true, SessionCap: 1}
	if err := st.PutAccount(account); err != nil {
		t.Fatal(err)
	}
	contractAuthenticate(t, svc, account)
	squad, err := svc.SaveSquad(domain.Squad{Name: "Core contract", Members: []domain.Member{{ID: "member", CLI: "codex", Model: "qa-native", AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := svc.CreateWorkspace("Core contract", root)
	if err != nil {
		t.Fatal(err)
	}
	mission, err := svc.CreateMission(workspace.ID, "Core contract", "Use approved bytes", squad.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindMission(mission.ID, service.MissionBindingRequest{WorkspaceID: workspace.ID, PlanPath: ".batuta/plans/alpha.md"}); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.PlanDetail(context.Background(), mission.ID, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := svc.ApprovePlan(context.Background(), detail.Identity)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(contractEventName(filepath.Join(root, ".git")))
	if err != nil {
		t.Fatal(err)
	}
	gate, err := windows.CreateEvent(nil, 1, 0, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(gate); err != nil {
			t.Error(err)
		}
	})
	return contractFixture{svc, approval, gate}
}

func contractAwait(t *testing.T, ctx context.Context, label string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", label, ctx.Err())
		case <-ticker.C:
		}
	}
}

func contractReadBirth(path, readyName string) ([]byte, bool, error) {
	name, err := windows.UTF16PtrFromString(readyName)
	if err != nil {
		return nil, false, err
	}
	ready, err := windows.OpenEvent(windows.SYNCHRONIZE, false, name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	status, waitErr := windows.WaitForSingleObject(ready, 0)
	closeErr := windows.CloseHandle(ready)
	if waitErr == nil && status != windows.WAIT_OBJECT_0 && status != uint32(windows.WAIT_TIMEOUT) {
		waitErr = fmt.Errorf("birth publication wait=%d", status)
	}
	if err := errors.Join(waitErr, closeErr); err != nil {
		return nil, false, err
	}
	if status == uint32(windows.WAIT_TIMEOUT) {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	return data, err == nil, err
}

func (f contractFixture) start(t *testing.T, ctx context.Context) (domain.ExecutionResult, *session.NativeExchangeTestObserver) {
	t.Helper()
	result, err := f.svc.StartExecution(ctx, domain.ExecutionRequest{Target: f.approval.Identity, ApprovalID: f.approval.ID})
	if err != nil || result.Effect != domain.ExecutionStarted || !domain.ValidExecutionRunID(result.RunID) || result.OpenedDigest == "" || result.Delivery != batuta.ExecutionDelivery(f.approval) {
		t.Fatal("public start identity", result, err)
	}
	var births contractBirths
	contractAwait(t, ctx, "provider birth", func() bool {
		common := filepath.Join(f.approval.Identity.Binding.CanonicalRoot, ".git")
		data, ready, err := contractReadBirth(filepath.Join(common, "core-contract-birth.json"), contractEventName(common)+"-birth-ready")
		if err != nil {
			t.Fatal(err)
		}
		if !ready {
			return false
		}
		if err := json.Unmarshal(data, &births); err != nil {
			t.Fatal(err)
		}
		return true
	})
	if !strings.EqualFold(filepath.Clean(births.Root), f.approval.Identity.Binding.CanonicalRoot) || births.Provider.PID == 0 || births.Provider.Creation == 0 || births.Provider.PID == births.Core.PID {
		t.Fatal("birth root/identity differs", births)
	}
	state, err := f.svc.ExecutionState(ctx, f.approval.Identity.MissionID, f.approval.Identity.Binding.WorkspaceID, f.approval.ID)
	if err != nil || !state.Active || state.RunID != result.RunID || state.Delivery != result.Delivery || state.OpenedDigest != result.OpenedDigest {
		t.Fatal("public run association", state, result, err)
	}
	observer, err := session.ObserveNativeExchangeForTest(f.svc.Session, births.Core.PID, births.Core.Creation)
	if err != nil {
		t.Fatal("exact original helper association", births, err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	before, err := observer.Snapshot()
	if err != nil || before.Forced || before.Retired || before.Exited || !before.Registered {
		t.Fatal("live original helper", before, err)
	}
	t.Logf("owner associated: approval=%s delivery=%s run=%s opened=%s birth=%+v", f.approval.ID, result.Delivery, result.RunID, result.OpenedDigest, births)
	return result, observer
}

func contractLockJournal(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	overlap := windows.Overlapped{Offset: (256 << 20) + 1}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap); err != nil {
		file.Close()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if err := errors.Join(windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlap), file.Close()); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	return release
}

func contractJoinedOwner(t *testing.T, observer *session.NativeExchangeTestObserver) session.NativeExchangeTestState {
	t.Helper()
	state, err := observer.Snapshot()
	if err != nil || state.TerminationError != nil || !state.Exited || !state.Signaled || !state.Retired || state.Registered {
		t.Fatal("original helper not joined", state, err)
	}
	return state
}

func TestNativeCoreServiceContractNoStop(t *testing.T) {
	image := contractImage(t)
	f := newContractFixture(t, image)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started, observer := f.start(t, ctx)
	if err := windows.SetEvent(f.gate); err != nil {
		t.Fatal(err)
	}
	contractAwait(t, ctx, "public completion without Stop", func() bool {
		events, err := f.svc.Store.EventsSince(0, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == "execution.finished" && event.Payload["delivery"] == started.Delivery {
				if event.Payload["failed"] != false || event.Payload["state"] != loop.StateWaitingInput {
					t.Fatal("Core completion event", event)
				}
				state, err := f.svc.ExecutionState(ctx, f.approval.Identity.MissionID, f.approval.Identity.Binding.WorkspaceID, f.approval.ID)
				if err != nil {
					t.Fatal(err)
				}
				return !state.Active && state.RunID == ""
			}
		}
		return false
	})
	state := contractJoinedOwner(t, observer)
	if state.Forced || state.ExitCode != 0 {
		t.Fatal("no-Stop helper used force", state)
	}
	binding := f.approval.Identity.Binding
	binding.DeliveryID, binding.OpenedDigest = started.Delivery, started.OpenedDigest
	evidence, err := batuta.ObserveApprovedExecutionContext(ctx, binding.CanonicalRoot, binding, f.approval)
	if err != nil || evidence.TerminalState != loop.StateWaitingInput || evidence.TerminalDigest == "" || evidence.CleanupPending {
		t.Fatal("no-Stop synchronized terminal", evidence, err)
	}
	t.Logf("no-Stop accepted Core state, successful real helper exit (Done/ACK path) and joined owner: %+v", state)
}

func TestNativeCoreServiceContractHeldJournalCancellation(t *testing.T) {
	image := contractImage(t)
	f := newContractFixture(t, image)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started, observer := f.start(t, ctx)
	origin := time.Now()
	journalPath := filepath.Join(f.approval.Identity.Binding.CanonicalRoot, ".batuta", "journal", started.Delivery+".jsonl")
	release := contractLockJournal(t, journalPath)
	heldAt := time.Since(origin)
	// The lock byte is beyond the journal data. Reading the stable bytes here
	// observes the stimulus; durable evidence is verified by Core after release.
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(before), []byte{'\n'}) {
		var record journal.Record
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Kind == loop.KindTerminal {
			t.Fatal("terminal preceded controlled cancellation")
		}
	}
	req := domain.ExecutionStopRequest{Target: f.approval.Identity, ApprovalID: f.approval.ID, Delivery: started.Delivery, RunID: started.RunID}
	stopAt := time.Since(origin)
	pending, err := f.svc.StopExecution(ctx, req)
	if err != nil || pending.Finalized || pending.Durable || pending.Effect != domain.ExecutionStopPending {
		t.Fatal("public Stop acceptance", pending, err)
	}
	acceptedAt := time.Since(origin)
	// This timer is the controlled journal stimulus, not a retry or a scheduling
	// workaround. The old owner's first cancellation observation is unavailable.
	timer := time.NewTimer(2750 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	heldOwner, observeErr := observer.Snapshot()
	sampledAt := time.Since(origin)
	heldBytes, readErr := os.ReadFile(journalPath)
	release()
	releasedAt := time.Since(origin)
	t.Logf("monotonic hold=%s Stop=%s accepted=%s sample=%s release=%s owner=%+v old-owner-cancel-observed=unavailable", heldAt, stopAt, acceptedAt, sampledAt, releasedAt, heldOwner)
	if observeErr != nil {
		t.Fatal("inconclusive owner observation", observeErr)
	}
	if readErr != nil || !bytes.Equal(before, heldBytes) {
		t.Fatal("journal changed during exclusive hold", readErr)
	}
	if releasedAt-acceptedAt <= 2*time.Second || releasedAt-stopAt >= 5*time.Second {
		t.Fatal("inconclusive controlled interval", releasedAt-stopAt)
	}
	var final domain.ExecutionStopResult
	contractAwait(t, ctx, "public finalization after journal release", func() bool {
		var err error
		final, err = f.svc.StopExecution(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return final.Finalized
	})
	joinedAt := time.Since(origin)
	state := contractJoinedOwner(t, observer)
	t.Logf("monotonic joined=%s original-owner=%+v final=%+v causes=%q", joinedAt, state, final, final.Causes)
	if final.ExecutionStopRequest != req {
		t.Fatal("final attempt identity changed", final)
	}
	if state.Forced {
		protocolFailure := false
		for _, cause := range final.Causes {
			if strings.Contains(cause, "EOF") || strings.Contains(cause, "broken pipe") {
				protocolFailure = true
			}
		}
		if !heldOwner.Forced || !heldOwner.Signaled || !heldOwner.Exited || !protocolFailure || final.CoreState != "" || final.Effect != domain.ExecutionStopUncertain || final.Durable {
			t.Fatalf("INCONCLUSIVE: force without the required old-policy causal evidence: held=%+v final=%+v", heldOwner, final)
		}
		t.Fatalf("CAUSAL RED: exact owner forced/signaled during held journal; joined retirement without termination error; protocol failed; no accepted Done/CoreState; final=%+v", final)
	}
	if final.Effect != domain.ExecutionStopCompleted || !final.Durable || final.Error != nil || final.CoreState != loop.StateCanceled || final.State == nil || final.State.Active || final.State.RunID != "" || final.State.Watch == nil || final.State.Watch.Header["state"] != loop.StateCanceled || state.ExitCode != 0 {
		t.Fatal("canceled finalization did not complete cooperatively", final, state)
	}
	binding := f.approval.Identity.Binding
	binding.DeliveryID, binding.OpenedDigest = started.Delivery, started.OpenedDigest
	evidence, err := batuta.ObserveApprovedExecutionContext(ctx, binding.CanonicalRoot, binding, f.approval)
	if err != nil || evidence.TerminalState != loop.StateCanceled || evidence.TerminalDigest == "" || evidence.CleanupPending {
		t.Fatal("canceled synchronized terminal", evidence, err)
	}
	t.Log("GREEN: synchronized canceled terminal, accepted Core state, real helper exit through Done/ACK and all owner joins without force")
}
