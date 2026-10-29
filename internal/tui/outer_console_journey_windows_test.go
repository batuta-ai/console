//go:build windows

package tui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/terminalengine"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/windows"
)

func TestOuterConsoleConPTYCatalogApprovalAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	f := windowsOuterFixture(t, ctx)
	const planPath = ".batuta/plans/alpha.md"
	const source = "# Plan — Alpha\n**Goal:** Preserve exact outer ConPTY bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n"
	workspace := filepath.Join(f.root, "workspace")
	windowsOuterWrite(t, filepath.Join(workspace, planPath), []byte(source))
	windowsOuterWrite(t, filepath.Join(workspace, ".batuta/profile.md"), []byte("# Profile\nStack: text\nMethodology: TDD\nTest: git diff --check\nBuild: none\nInstall: none\nExecution: sequential\nWorktree: always\nTemplate: templates/generic.md\n"))
	windowsOuterWrite(t, filepath.Join(workspace, ".batuta/routing.md"), []byte("| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | fixture |\n| medium | * | codex | fixture |\n| high | * | claude | sonnet |\n| critical | * | self | session |\n"))
	windowsOuterWrite(t, filepath.Join(workspace, "WORK.md"), []byte("# Work\n\n## In progress\n\n## Done\n"))
	decoyRoot := filepath.Join(f.root, "other-workspace")
	windowsOuterDirectory(t, decoyRoot)
	decoy, err := f.api.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "Other workspace", Root: decoyRoot})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.api.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "Outer ConPTY", Root: workspace})
	if err != nil {
		t.Fatal(err)
	}
	squad, err := f.api.SaveSquad(ctx, domain.Squad{Name: "Credentialless catalog squad"})
	if err != nil {
		t.Fatal(err)
	}
	var missions []domain.Mission
	for _, title := range []string{"Other mission", "Selected mission"} {
		m, err := f.api.CreateMission(ctx, client.CreateMissionRequest{WorkspaceID: ws.ID, Title: title, Brief: "Exact " + title, SquadID: squad.ID})
		if err != nil {
			t.Fatal(err)
		}
		missions = append(missions, m)
	}
	other, selected := missions[0], missions[1]
	if other.ID == selected.ID || other.ID == "" || selected.ID == "" || ws.ID == decoy.ID {
		t.Fatal("distinct catalog identities required")
	}
	console := f.start(t, ctx, workspace)
	selectWorkspace := func(want domain.Workspace) {
		t.Helper()
		console.key(t, "w")
		s := console.screen(t, ctx, "workspace catalog", func(s string) bool {
			return strings.Contains(s, ws.ID) && strings.Contains(s, decoy.ID) && strings.Contains(s, "enter details")
		})
		console.selectRow(t, ctx, s, "keyboard workspace "+want.ID, want.ID)
		console.key(t, "\r")
		console.screen(t, ctx, "workspace detail "+want.ID, func(s string) bool {
			return strings.Contains(s, want.ID+"  "+want.Name) && strings.Contains(s, want.Root) && strings.Contains(s, "Setup:")
		})
		console.key(t, "\x1b")
		console.contains(t, ctx, "enter details")
	}
	selectWorkspace(decoy)
	selectWorkspace(ws)
	selectMission := func(want domain.Mission) {
		t.Helper()
		console.key(t, "m")
		s := console.screen(t, ctx, "mission list", func(s string) bool {
			return strings.Contains(s, "OBJECTIVE") && strings.Contains(s, other.ID) && strings.Contains(s, selected.ID)
		})
		console.selectRow(t, ctx, s, "keyboard-selected "+want.ID, want.ID)
		console.key(t, "\r")
		console.screen(t, ctx, "room "+want.ID, func(s string) bool {
			return strings.Contains(s, want.ID+"  Workspace: "+ws.ID) && strings.Contains(s, want.Brief) && strings.Contains(s, "p interactive planning")
		})
	}
	selectMission(other)
	selectMission(selected)
	console.key(t, "b")
	console.contains(t, ctx, "Exact existing plan path")
	console.key(t, planPath)
	console.contains(t, ctx, strconv.Quote(planPath))
	console.key(t, "\t\t\r")
	console.contains(t, ctx, "Confirm: Bind an existing exact plan")
	console.key(t, "\r")
	console.screen(t, ctx, "bound selected mission", func(s string) bool {
		return strings.Contains(s, selected.ID+"  Workspace: "+ws.ID) && strings.Contains(s, "\\\"workspace_id\\\": \\\""+ws.ID+"\\\"") && !strings.Contains(s, "Mission request pending") && !strings.Contains(s, "Confirm:")
	})
	detail, err := f.api.PlanDetail(ctx, selected.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	binding := detail.Identity.Binding
	if detail.Identity.MissionID != selected.ID || binding.WorkspaceID != ws.ID || binding.WorkspaceRoot != ws.Root || binding.CanonicalRoot != canonical || binding.PlanPath != planPath || binding.PlanSlug != "alpha" || binding.PlanDigest == "" || binding.DeliveryID != "" || binding.OpenedDigest != "" || detail.Approval != nil || string(detail.Content) != source || detail.Identity.Version != domain.PlanVersion([]byte(source)) {
		t.Fatalf("unexpected exact bound plan: %+v", detail)
	}
	console.key(t, "o")
	windowsOuterReviewSource(t, ctx, console, detail)
	console.key(t, "y")
	outerInspectionReviewJSON(t, ctx, console, "Confirm: Approve all inspected original source bytes", detail.Identity)
	unapproved, err := f.api.PlanDetail(ctx, selected.ID, ws.ID)
	if err != nil || unapproved.Approval != nil || !reflect.DeepEqual(unapproved, detail) {
		t.Fatalf("source inspection or confirmation prompt approved/mutated without Enter: %+v %v", unapproved, err)
	}
	console.key(t, "\r")
	console.screen(t, ctx, "approved selected mission", func(s string) bool {
		return strings.Contains(s, "Immutable approved snapshot") && !strings.Contains(s, "Mission request pending") && !strings.Contains(s, "Confirm:")
	})
	approved, err := f.api.PlanDetail(ctx, selected.ID, ws.ID)
	if err != nil || approved.Approval == nil {
		t.Fatalf("approval reread: %+v %v", approved, err)
	}
	receipt := *approved.Approval
	if receipt.Identity != detail.Identity || string(receipt.Content) != source || receipt.ID == "" || receipt.ApprovedAt.IsZero() {
		t.Fatalf("approval identity/bytes: %+v", receipt)
	}
	selected.Binding = &binding
	checkCatalog := func() {
		t.Helper()
		got, err := f.api.ListMissions(ctx, ws.ID)
		if err != nil || len(got) != 2 {
			t.Fatalf("two-mission reread: %+v %v", got, err)
		}
		for _, want := range []domain.Mission{other, selected} {
			found := false
			for _, m := range got {
				if m.ID == want.ID {
					found = true
					if !reflect.DeepEqual(m, want) {
						t.Fatalf("mission changed: got=%+v want=%+v", m, want)
					}
				}
			}
			if !found {
				t.Fatalf("missing mission %s", want.ID)
			}
		}
		gotReceipt, err := f.api.PlanApproval(ctx, selected.ID, ws.ID, receipt.ID)
		if err != nil || !reflect.DeepEqual(gotReceipt, receipt) {
			t.Fatalf("immutable approval reread: %+v %v", gotReceipt, err)
		}
		_, err = f.api.PlanApproval(ctx, other.ID, ws.ID, receipt.ID)
		var missing *client.PlanError
		if !errors.As(err, &missing) || missing.Status != http.StatusNotFound {
			t.Fatalf("other mission approval must be absent: %v", err)
		}
		storedMission, err := f.store.GetMission(selected.ID)
		if err != nil || !reflect.DeepEqual(storedMission, selected) {
			t.Fatalf("HTTP/Store mission disagree: %+v %v", storedMission, err)
		}
		storedReceipt, err := f.store.GetPlanApproval(ctx, selected.ID, ws.ID, receipt.ID)
		if err != nil || !reflect.DeepEqual(storedReceipt, receipt) {
			t.Fatalf("HTTP/Store receipt disagree: %+v %v", storedReceipt, err)
		}
		raw, err := json.Marshal(gotReceipt)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("HTTP/Store exact receipt JSON %s", raw)
		t.Logf("HTTP exact missions=%s,%s binding=%+v approval=%s version=%s", other.ID, selected.ID, binding, receipt.ID, receipt.Identity.Version)
	}
	checkCatalog()
	refresh := func(m domain.Mission) {
		t.Helper()
		console.key(t, "f")
		console.screen(t, ctx, "refreshed room "+m.ID, func(s string) bool {
			if !strings.Contains(s, m.ID+"  Workspace: "+ws.ID) || !strings.Contains(s, "Last verified effect: read_only") || strings.Contains(s, "Mission request pending") || strings.Contains(s, "refresh required") || strings.Contains(s, "Request failed") {
				return false
			}
			if m.ID == selected.ID {
				return strings.Contains(s, "Immutable approved snapshot")
			}
			return !strings.Contains(s, "Immutable approved snapshot")
		})
		if m.ID == selected.ID {
			console.key(t, "k")
			outerInspectionReviewJSON(t, ctx, console, "Immutable approved snapshot", receipt)
			console.key(t, "\x1b")
			console.contains(t, ctx, m.ID+"  Workspace: "+ws.ID)
		}
	}
	refresh(selected)

	original, err := os.ReadFile(filepath.Join(workspace, planPath))
	if err != nil || !bytes.Equal(original, []byte(source)) {
		t.Fatalf("approval altered original source: %q %v", original, err)
	}
	before, err := f.api.Activity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	console.quit(t, ctx)
	first := console.pending.Snapshot()
	console = f.start(t, ctx, workspace)
	second := console.pending.Snapshot()
	if first.ID == second.ID || first.ProcessIdentity == second.ProcessIdentity {
		t.Fatal("restart reused a retained Console incarnation")
	}
	// Selection is deliberately made again by keyboard; a fresh Console does not
	// persist UI cursor state. The persisted mission and receipt must survive it.
	selectWorkspace(ws)
	selectMission(other)
	refresh(other)
	selectMission(selected)
	refresh(selected)
	checkCatalog()
	after, err := f.api.Activity(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("restart/inspection repeated mutation: before=%+v after=%+v err=%v", before, after, err)
	}
	original, err = os.ReadFile(filepath.Join(workspace, planPath))
	if err != nil || !bytes.Equal(original, []byte(source)) {
		t.Fatalf("restart altered original source: %q %v", original, err)
	}
	sessions, err := f.api.ListSessions(ctx)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("unexpected inner sessions: %+v %v", sessions, err)
	}
	providerAccounts, err := f.api.ListAccounts(ctx)
	if err != nil || len(providerAccounts) != 0 {
		t.Fatalf("unexpected provider accounts: %+v %v", providerAccounts, err)
	}
	console.quit(t, ctx)
	f.finish(t, ctx)
}

func windowsOuterSelectionRows(screen, id string) (marked, target int) {
	marked, target = -1, -1
	for row, line := range strings.Split(screen, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == ">" {
			if marked != -1 {
				return -1, -1
			}
			marked = row
		}
		for _, field := range fields {
			if field == id {
				if target != -1 {
					return -1, -1
				}
				target = row
			}
		}
	}
	return marked, target
}

func (p *windowsOuterConsole) selectRow(t *testing.T, ctx context.Context, screen, label, id string) {
	t.Helper()
	marked, target := windowsOuterSelectionRows(screen, id)
	if marked < 0 || target < 0 {
		t.Fatalf("missing or ambiguous rendered selection for %s\n%s", id, screen)
	}
	if marked == target {
		return
	}
	key := "\x1b[B"
	if target < marked {
		key = "\x1b[A"
	}
	// Each fixture catalog has two rows; select by their observed order.
	p.key(t, key)
	p.screen(t, ctx, label, func(s string) bool {
		marked, target := windowsOuterSelectionRows(s, id)
		return target >= 0 && marked == target
	})
}

// All children are controlled by retained Manager/Job authority. PID and birth
// identity are evidence only, never termination or rediscovery authority.
type windowsOuterConsole struct {
	witness windows.Handle
	manager *session.Manager
	pending *session.Pending
	sub     *session.TerminalSubscription
	ctx     context.Context
	done    chan struct{}
	changed chan struct{}
	mu      sync.Mutex
	raw     bytes.Buffer
	view    string
	readErr error
	eof     bool
}

type windowsOuterHarness struct {
	root, home, binary, address string
	env                         []string
	api                         *client.Client
	store                       *store.Store
	svc                         *service.Service
	inner                       *session.Manager
	consoles                    []*windowsOuterConsole
	server                      *http.Server
	served                      chan error
	transport                   *http.Transport
	handlers                    sync.WaitGroup
	handlerMu                   sync.Mutex
	closing                     bool
	cancelHTTP                  context.CancelFunc
	finishOnce                  sync.Once
	finishDone                  chan struct{}
	finishErr                   error
	reported                    bool
	httpLog                     bytes.Buffer
	buildComplete               bool
}

func windowsOuterFixture(t *testing.T, ctx context.Context) *windowsOuterHarness {
	t.Helper()
	root := filepath.Join(os.TempDir(), "console-outer-"+rand.Text())
	// Create the fresh root with its final protected current-user DACL at birth.
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatal(err)
	}
	f := &windowsOuterHarness{root: root, finishDone: make(chan struct{})}
	// Register last, after environment restoration registrations, so cleanup runs
	// while the same private identity remains selected.
	defer func() {
		t.Cleanup(func() {
			deadline, ok := t.Deadline()
			if !ok {
				t.Errorf("missing test deadline; retaining %s", root)
				return
			}
			drain, done := context.WithDeadline(context.Background(), deadline)
			defer done()
			f.finish(t, drain)
		})
	}()
	windowsOuterDirectory(t, root)
	goRoot := runtime.GOROOT()
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		t.Fatal("pinned GOMODCACHE is required; no implicit cache/download")
	}
	systemRoot, systemDrive := os.Getenv("SYSTEMROOT"), os.Getenv("SYSTEMDRIVE")
	// Clear every ambient entry before installing an allowlisted private identity.
	// This includes provider keys, proxy settings, Git configuration and CLI homes.
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			t.Setenv(key, "")
		}
	}
	userHome := filepath.Join(root, "user")
	bin := filepath.Join(root, "bin")
	f.env = []string{"SYSTEMROOT=" + systemRoot, "WINDIR=" + systemRoot, "SYSTEMDRIVE=" + systemDrive,
		"PATH=" + bin, "HOME=" + userHome, "USERPROFILE=" + userHome,
		"HOMEDRIVE=" + filepath.VolumeName(userHome), "HOMEPATH=" + strings.TrimPrefix(userHome, filepath.VolumeName(userHome)),
		"APPDATA=" + filepath.Join(userHome, "roaming"), "LOCALAPPDATA=" + filepath.Join(userHome, "local"),
		"TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"), "TMPDIR=" + filepath.Join(root, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"CODEX_HOME=" + filepath.Join(root, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude"), "CURSOR_CONFIG_DIR=" + filepath.Join(root, "cursor"),
		"BATUTA_SKILLS=" + filepath.Join(root, "skills"), "LANG=C", "LC_ALL=C", "TERM=xterm-256color"}
	for _, entry := range f.env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(value, root) && key != "HOMEPATH" {
			windowsOuterDirectory(t, value)
		}
		t.Setenv(key, value)
	}
	t.Setenv("BATUTA_LANG", "en")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	f.env = accounts.RuntimeEnvironment(nil, f.env)
	f.binary = windowsOuterBuild(t, ctx, root, moduleCache, goRoot, f.env)
	f.buildComplete = true
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("environment: actual owned loopback listener: %v", err)
	}
	f.address = listener.Addr().String()
	// Close this handle on initialization failure too; Serve owns it thereafter.
	t.Cleanup(func() { _ = listener.Close() })
	paths, err := consoleruntime.Resolve(filepath.Join(root, "runtime"), f.address)
	if err != nil {
		t.Fatal(err)
	}
	f.home = paths.Home
	token, err := consoleruntime.LoadOrCreateAuth(paths.Auth)
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = store.Open(paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	f.inner = session.New(paths.Logs)
	f.svc = service.New(f.store, f.inner, nil)
	f.svc.Home = paths.Home
	f.svc.CoreExecutable = filepath.Join(bin, "absent-core.exe")
	handler := (&api.Server{Svc: f.svc, Token: token}).Handler()
	httpContext, cancelHTTP := context.WithCancel(context.Background())
	f.cancelHTTP = cancelHTTP
	f.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return httpContext }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handlerMu.Lock()
		if f.closing {
			f.handlerMu.Unlock()
			http.Error(w, "fixture closing", http.StatusServiceUnavailable)
			return
		}
		f.handlers.Add(1)
		if f.httpLog.Len() < 1<<20 {
			fmt.Fprintf(&f.httpLog, "%s %s\n", r.Method, r.URL.Path)
		}
		f.handlerMu.Unlock()
		defer f.handlers.Done()
		handler.ServeHTTP(w, r)
	})}
	f.served = make(chan error, 1)
	go func() { f.served <- f.server.Serve(listener) }()
	f.transport = &http.Transport{Proxy: nil}
	f.api = client.New("http://"+f.address, token)
	f.api.HTTP = &http.Client{Transport: f.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if status := f.api.Probe(); status != client.StatusReady {
		t.Fatalf("real authenticated API not ready: %v", status)
	}
	t.Logf("HTTP actual api.Server/Service/Store endpoint=%s home=%s; private auth; empty provider catalog", f.address, f.home)
	return f
}

func windowsOuterDirectory(t *testing.T, path string) {
	t.Helper()
	if err := consoleruntime.EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
}

func windowsOuterWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	windowsOuterDirectory(t, filepath.Dir(path))
	file, err := consoleruntime.CreatePrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
}

func windowsOuterBuild(t *testing.T, ctx context.Context, root, moduleCache, goRoot string, env []string) string {
	t.Helper()
	if runtime.Version() != "go1.27.3" {
		t.Fatalf("requires pinned native Go 1.27.3, got %s", runtime.Version())
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		t.Fatal("native runtime.Caller source path required")
	}
	project := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	var manifest bytes.Buffer
	err := filepath.WalkDir(project, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".batuta", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("nonregular source input %s", path)
		}
		rel, err := filepath.Rel(project, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(data), filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	windowsOuterWrite(t, filepath.Join(root, "source-manifest.sha256"), manifest.Bytes())
	binary := filepath.Join(root, "output", "console.exe")
	windowsOuterDirectory(t, filepath.Dir(binary))
	buildEnv := append(append([]string{}, env...), "GOROOT="+goRoot, "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off", "GOMODCACHE="+moduleCache, "GOCACHE="+filepath.Join(root, "go-cache"), "CGO_ENABLED=0", "GOOS=windows", "GOARCH="+runtime.GOARCH)
	windowsOuterDirectory(t, filepath.Join(root, "go-cache"))
	command := exec.CommandContext(ctx, filepath.Join(goRoot, "bin", "go.exe"), "build", "-buildvcs=false", "-o", binary, "./cmd/console")
	command.Env, command.Dir = buildEnv, project
	command.WaitDelay = 5 * time.Second
	t.Logf("BUILD native dir=%s argv=%q source_manifest_sha256=%x VCS=unknown (buildvcs=false; source archive needs no .git)", project, command.Args, sha256.Sum256(manifest.Bytes()))
	raw, err := command.CombinedOutput()
	windowsOuterWrite(t, filepath.Join(root, "build.log"), raw)
	if err != nil {
		t.Fatalf("actual native Console build: %v; retaining build dependencies at %s\n%s", err, root, raw)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.GoVersion != "go1.27.3" || info.Path != "github.com/batuta-ai/console/cmd/console" {
		t.Fatalf("binary provenance mismatch: %s", info)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	provenance := fmt.Sprintf("source=%s\nmanifest_sha256=%x\nbinary=%s\nbinary_sha256=%x\n%s", project, sha256.Sum256(manifest.Bytes()), binary, sha256.Sum256(data), info)
	windowsOuterWrite(t, filepath.Join(root, "provenance.txt"), []byte(provenance))
	t.Logf("BUILD exit=0 Wait joined\n%s", provenance)
	for _, candidate := range []string{filepath.Join(root, "bin", "consoled.exe"), filepath.Join(filepath.Dir(binary), "consoled.exe")} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("daemon autostart candidate exists: %s %v", candidate, err)
		}
	}
	return binary
}

func (f *windowsOuterHarness) start(t *testing.T, ctx context.Context, workspace string) *windowsOuterConsole {
	t.Helper()
	id := fmt.Sprintf("outer-console-%d", len(f.consoles)+1)
	logs := filepath.Join(f.root, id)
	windowsOuterDirectory(t, logs)
	p := &windowsOuterConsole{manager: session.New(logs), ctx: ctx, done: make(chan struct{}), changed: make(chan struct{}, 1)}
	f.consoles = append(f.consoles, p)
	argv := []string{f.binary, "-home", f.home, "-listen", f.address}
	pending, err := p.manager.StartPending(domain.Session{ID: id, Argv: argv, Rows: 80, Cols: 240, Owner: "tui", OwnerClient: "outer-qualification"}, f.env, workspace, 0)
	if err != nil {
		close(p.done)
		t.Fatalf("actual ordinary ConPTY startup: %v", err)
	}
	p.pending = pending
	p.sub, err = p.manager.SubscribeTerminal(id)
	if err != nil {
		close(p.done)
		t.Fatal(err)
	}
	snap := pending.Snapshot()
	t.Logf("PROCESS actual ConPTY session=%s pid=%d incarnation=%s argv=%q size=240x80", id, snap.PID, snap.ProcessIdentity, argv)
	go p.read()
	// This query-only witness is bound to the Manager's observed incarnation;
	// all termination remains with the Manager's original native Job.
	p.witness, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(snap.PID))
	if err != nil {
		t.Fatal(err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.witness, &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	identity := fmt.Sprintf("windows:%d:%d", snap.PID, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	if identity != snap.ProcessIdentity {
		t.Fatalf("process incarnation changed: observed=%s wanted=%s", identity, snap.ProcessIdentity)
	}
	if snap.PID <= 0 || snap.ProcessIdentity == "" {
		t.Fatal("native process birth observation missing")
	}
	p.contains(t, ctx, "w workspaces (select / create / setup)")
	return p
}

func (p *windowsOuterConsole) read() {
	defer close(p.done)
	screen := terminalengine.NewScreen(80, 240)
	screen.SetScrollbackLimit(0)
	parser := terminalengine.NewParser(screen)
	for {
		event, err := p.sub.Next(context.Background())
		p.mu.Lock()
		if event.Cols != 0 && (event.Cols != 240 || event.Rows != 80) {
			p.readErr = fmt.Errorf("unexpected native geometry: %dx%d", event.Cols, event.Rows)
		}
		if len(event.Data) > 0 {
			if p.raw.Len()+len(event.Data) <= 4<<20 {
				p.raw.Write(event.Data)
			} else {
				p.readErr = errors.New("outer Console exceeded 4 MiB output bound")
			}
			parser.Feed(event.Data)
			p.view = ansi.Strip(screen.Render())
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				p.eof = true
			} else {
				p.readErr = errors.Join(p.readErr, err)
			}
		}
		p.mu.Unlock()
		select {
		case p.changed <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

func (p *windowsOuterConsole) key(t *testing.T, keys string) {
	t.Helper()
	id := p.pending.Snapshot().ID
	t.Logf("KEY session=%s bytes=%q", id, keys)
	if err := p.manager.InputContext(p.ctx, id, "outer-qualification", []byte(keys)); err != nil {
		t.Fatal(err)
	}
}

func (p *windowsOuterConsole) screen(t *testing.T, ctx context.Context, label string, matches func(string) bool) string {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		p.mu.Lock()
		view, readErr := p.view, p.readErr
		p.mu.Unlock()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if matches(view) && !strings.Contains(view, "Mission request pending") {
			t.Logf("SCREEN %s\n%s", label, strings.TrimSpace(view))
			return view
		}
		select {
		case <-p.changed:
		case <-p.done:
			t.Fatalf("Console ended before %s\n%s", label, view)
		case <-deadline.C:
			t.Fatalf("screen deadline %s\n%s", label, view)
		case <-ctx.Done():
			t.Fatalf("journey deadline %s: %v\n%s", label, ctx.Err(), view)
		}
	}
}

func (p *windowsOuterConsole) contains(t *testing.T, ctx context.Context, text string) string {
	t.Helper()
	return p.screen(t, ctx, text, func(s string) bool { return strings.Contains(s, text) })
}

func (p *windowsOuterConsole) quit(t *testing.T, ctx context.Context) {
	t.Helper()
	p.key(t, "q")
	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case <-p.done:
	case <-wait.Done():
		t.Fatalf("keyboard quit did not drain actual ConPTY subscription: %v", wait.Err())
	}
	p.mu.Lock()
	eof, readErr := p.eof, p.readErr
	p.mu.Unlock()
	if !eof || readErr != nil {
		t.Fatalf("actual subscription EOF=%t error=%v", eof, readErr)
	}
	// EOF follows native process/output retirement. Shutdown additionally joins
	// callback/log/private-home finalization, using the same retained Manager.
	if err := p.manager.Shutdown(wait); err != nil {
		t.Fatalf("outer full finalizer join: %v", err)
	}
	snap := p.pending.Snapshot()
	if snap.Process != "exited" || snap.EndedAt == nil {
		t.Fatalf("Console did not exit: %+v", snap)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(p.witness, &exitCode); err != nil || exitCode != 0 {
		t.Fatalf("keyboard Console exit=%d error=%v", exitCode, err)
	}
	if status, err := windows.WaitForSingleObject(p.witness, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("original Console handle still live: %d %v", status, err)
	}
	p.sub.Close()
	t.Logf("JOIN keyboard quit session=%s incarnation=%s: subscription EOF, reader, native Job, Manager finalizers; snapshot=%+v", snap.ID, snap.ProcessIdentity, snap)
}

func (f *windowsOuterHarness) finish(t *testing.T, ctx context.Context) {
	t.Helper()
	f.finishOnce.Do(func() {
		go func() {
			defer close(f.finishDone)
			var joinedErr error
			for _, p := range f.consoles {
				err := p.manager.Shutdown(context.Background())
				joinedErr = errors.Join(joinedErr, err)
				<-p.done
				if p.witness != 0 {
					joinedErr = errors.Join(joinedErr, windows.CloseHandle(p.witness))
				}
				if p.sub != nil {
					p.sub.Close()
				}
				p.mu.Lock()
				raw, readErr := append([]byte{}, p.raw.Bytes()...), p.readErr
				p.mu.Unlock()
				joinedErr = errors.Join(joinedErr, readErr)
				if p.pending != nil {
					path := filepath.Join(f.root, p.pending.Snapshot().ID+".terminal.raw")
					joinedErr = errors.Join(joinedErr, os.WriteFile(path, raw, 0600))
				}
			}
			if f.server != nil {
				f.handlerMu.Lock()
				f.closing = true
				f.handlerMu.Unlock()
				f.cancelHTTP()
				closeErr := f.server.Close()
				serveErr := <-f.served
				if errors.Is(serveErr, http.ErrServerClosed) {
					serveErr = nil
				}
				f.handlers.Wait()
				f.transport.CloseIdleConnections()
				joinedErr = errors.Join(joinedErr, closeErr, serveErr)
			}
			if f.svc != nil {
				joinedErr = errors.Join(joinedErr, f.svc.ShutdownNative(context.Background()))
			}
			if f.inner != nil {
				joinedErr = errors.Join(joinedErr, f.inner.Shutdown(context.Background()))
			}
			// No Store close or root removal until every actual join above has returned.
			// Cleanup errors also retain the Store and artifacts for Root inspection.
			if joinedErr == nil && f.buildComplete && f.store != nil {
				joinedErr = f.store.Close()
			}
			f.finishErr = joinedErr
		}()
	})
	select {
	case <-f.finishDone:
		if f.reported {
			return
		}
		f.reported = true
		for _, p := range f.consoles {
			p.mu.Lock()
			raw := p.raw.String()
			p.mu.Unlock()
			t.Logf("RAW actual ConPTY transcript %q", raw)
		}
		f.handlerMu.Lock()
		httpProof := f.httpLog.String()
		f.handlerMu.Unlock()
		t.Logf("HTTP actual requests (no headers/secrets)\n%s", httpProof)
		if f.finishErr != nil {
			t.Errorf("owned cleanup failed; retaining %s: %v", f.root, f.finishErr)
			return
		}
		if !f.buildComplete {
			t.Logf("build completion unproven; retaining %s", f.root)
			return
		}
		t.Logf("JOIN all outer Jobs/readers/subscriptions, HTTP handlers/streams/Serve, Service native work and inner Manager before Store.Close; artifacts=%s", f.root)
		if !t.Failed() {
			if err := os.RemoveAll(f.root); err != nil {
				t.Error(err)
			}
		} else {
			t.Logf("failed-attempt artifacts retained at %s", f.root)
		}
	case <-ctx.Done():
		t.Errorf("strict cleanup deadline: %v; retaining live Store/dependencies/artifacts at %s", ctx.Err(), f.root)
	}
}

func windowsOuterReviewSource(t *testing.T, ctx context.Context, p *windowsOuterConsole, detail domain.PlanDetail) {
	t.Helper()
	metadata, source := outerInspectionRead(t, ctx, p, "Current source", detail.Identity.Version)
	var decoded domain.PlanDetail
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("visible metadata: %v\n%s", err, metadata)
	}
	want := detail
	want.Content, want.Approval = nil, nil
	if !reflect.DeepEqual(decoded, want) || !bytes.Equal(source, detail.Content) {
		t.Fatalf("visible source differs from independently read exact HTTP bytes: metadata=%+v source=%q", decoded, source)
	}
	t.Logf("REVIEW exact metadata and %d source bytes sha256=%x", len(source), sha256.Sum256(source))
}

type outerInspectionConsole interface {
	screen(*testing.T, context.Context, string, func(string) bool) string
	key(*testing.T, string)
}

// The indicator counts physical rows; byte offsets are independent per document.
func outerInspectionRange(screen string, next, total int) (first, last, count int, ok bool) {
	match := regexp.MustCompile(`(?m)^\s*Rows (\d+)-(\d+)/(\d+)\s*$`).FindStringSubmatch(screen)
	if len(match) == 0 {
		return
	}
	first, _ = strconv.Atoi(match[1])
	last, _ = strconv.Atoi(match[2])
	count, _ = strconv.Atoi(match[3])
	ok = first == next && last >= first && last-first < 8 && last <= count &&
		(total == 0 || count == total) && regexp.MustCompile(`(?m)^[ \t]*END OF SOURCE[ \t]*$`).MatchString(screen) == (last == count)
	return
}

func outerInspectionAppend(target []byte, row string) ([]byte, error) {
	parts := regexp.MustCompile(`^([0-9a-f]{8}) (".*")$`).FindStringSubmatch(row)
	if len(parts) == 0 {
		return nil, fmt.Errorf("invalid visible byte row: %q", row)
	}
	for _, c := range row {
		if c < 32 || c > 126 {
			return nil, fmt.Errorf("non-ASCII/control in visible byte row: %q", row)
		}
	}
	offset, err := strconv.ParseInt(parts[1], 16, 64)
	if err != nil || offset != int64(len(target)) {
		return nil, fmt.Errorf("visible byte gap/overlap: offset=%d collected=%d: %v", offset, len(target), err)
	}
	decoded, err := strconv.Unquote(parts[2])
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("invalid/empty quoted byte chunk: %q: %v", parts[2], err)
	}
	return append(target, decoded...), nil
}

func outerInspectionRead(t *testing.T, ctx context.Context, p outerInspectionConsole, title, sourceVersion string) (metadata, source []byte) {
	t.Helper()
	next, total, inSource, sawMetadata := 1, 0, false, false
	for {
		s := p.screen(t, ctx, "next exact inspection page: "+title, func(s string) bool {
			_, _, _, ok := outerInspectionRange(s, next, total)
			if !ok || strings.TrimSpace(strings.Split(strings.TrimSpace(s), "\n")[0]) != title || !strings.Contains(s, "up/down pgup/pgdown pages") {
				return false
			}
			if sourceVersion != "" {
				for _, line := range strings.Split(s, "\n") {
					if strings.TrimSpace(line) == sourceVersion {
						return true
					}
				}
				return false
			}
			return true
		})
		first, last, count, _ := outerInspectionRange(s, next, total)
		lines := strings.Split(s, "\n")
		indicator := -1
		for i, line := range lines {
			if strings.TrimSpace(line) == fmt.Sprintf("Rows %d-%d/%d", first, last, count) {
				indicator = i
				break
			}
		}
		bodyStart := indicator - (last - first + 1)
		if bodyStart < 0 {
			t.Fatal("page indicator exceeds physically visible body")
		}
		for _, line := range lines[bodyStart:indicator] {
			line = strings.TrimSpace(line)
			switch line {
			case "Full metadata/tasks (ASCII escaped JSON bytes)":
				if sourceVersion == "" || sawMetadata || inSource || first != 1 {
					t.Fatal("unexpected/repeated metadata section")
				}
				sawMetadata = true
			case "Original source bytes; offsets and lossless ASCII escapes":
				if sourceVersion == "" || !sawMetadata || inSource {
					t.Fatal("unexpected/repeated original byte section")
				}
				inSource = true
			default:
				target := &metadata
				if inSource {
					target = &source
				}
				var err error
				*target, err = outerInspectionAppend(*target, line)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if last == count {
			if sourceVersion != "" && (!sawMetadata || !inSource) {
				t.Fatal("source ended without both exact byte sections")
			}
			return
		}
		next, total = last+1, count
		p.key(t, "\x1b[B")
	}
}

func outerInspectionReviewJSON(t *testing.T, ctx context.Context, p outerInspectionConsole, title string, want any) {
	t.Helper()
	raw, _ := outerInspectionRead(t, ctx, p, title, "")
	expected, err := json.MarshalIndent(want, "", "  ")
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatalf("visible complete %s JSON differs: got=%s want=%s error=%v", title, raw, expected, err)
	}
	t.Logf("REVIEW exact %s JSON bytes=%d sha256=%x", title, len(raw), sha256.Sum256(raw))
}

func TestOuterInspectionPageProtocol(t *testing.T) {
	firstPage := "Current source\nversion\n00000000 \"abc\"\nRows 1-1/2\nup/down pgup/pgdown pages  esc back\n"
	lastPage := "Current source\nversion\n00000003 \"def\"\nRows 2-2/2\nEND OF SOURCE\nup/down pgup/pgdown pages  esc back\n"
	for _, tc := range []struct {
		name, screen                    string
		next, total, first, last, count int
		ok                              bool
	}{
		{"first", firstPage, 1, 0, 1, 1, 2, true},
		{"next", lastPage, 2, 2, 2, 2, 2, true},
		{"marker inside data", strings.ReplaceAll(firstPage, "abc", "END OF SOURCE"), 1, 0, 1, 1, 2, true},
		{"cached", firstPage, 2, 2, 0, 0, 0, false},
		{"changed total", lastPage, 2, 3, 0, 0, 0, false},
		{"skipped", lastPage, 1, 2, 0, 0, 0, false},
		{"premature end", firstPage + "END OF SOURCE\n", 1, 0, 0, 0, 0, false},
		{"missing end", strings.ReplaceAll(lastPage, "END OF SOURCE\n", ""), 2, 2, 0, 0, 0, false},
		{"oversized", strings.ReplaceAll(firstPage, "1-1/2", "1-9/10"), 1, 0, 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, last, count, ok := outerInspectionRange(tc.screen, tc.next, tc.total)
			if ok != tc.ok || ok && (first != tc.first || last != tc.last || count != tc.count) {
				t.Fatalf("page=(%d,%d,%d,%t), want=(%d,%d,%d,%t)", first, last, count, ok, tc.first, tc.last, tc.count, tc.ok)
			}
		})
	}
}

func TestOuterInspectionByteReassembly(t *testing.T) {
	// Chunks deliberately split a UTF-8 rune and include invalid source bytes.
	rows := []string{`00000000 "A\xe2"`, `00000002 "\x86\x92\n\x1b[31m\xff"`}
	var got []byte
	for _, row := range rows {
		var err error
		got, err = outerInspectionAppend(got, row)
		if err != nil {
			t.Fatal(err)
		}
	}
	if want := []byte("A→\n\x1b[31m\xff"); !bytes.Equal(got, want) {
		t.Fatalf("reassembled bytes=%q want=%q", got, want)
	}
	for _, row := range []string{
		`00000000 "duplicate"`, `00000003 "gap"`, `00000002 ""`,
		`00000002 "\q"`, `00000002 "unterminated`, "00000002 \"\x1b[31m\"", `00000002 "→"`,
	} {
		if _, err := outerInspectionAppend([]byte("ab"), row); err == nil {
			t.Errorf("accepted invalid/gapped/overlapping visible row %q", row)
		}
	}
}
