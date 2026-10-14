//go:build darwin || linux

package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
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
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/terminalengine"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

func TestOuterConsolePTYCatalogApprovalAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "console-outer-journey-")
	if err != nil {
		t.Fatal(err)
	}
	var processes []*outerJourneyProcess
	t.Cleanup(func() {
		for _, p := range processes {
			if !p.joined {
				t.Errorf("unjoined owned process/reader; retaining artifacts at %s", root)
				return
			}
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("owned temporary cleanup: %v", err)
		}
	})
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	home, workspace, bin := filepath.Join(root, "runtime"), filepath.Join(root, "workspace"), filepath.Join(root, "bin")
	// Separate outputs also prevent Console's daemon auto-start fallback from
	// creating an unretained process if the owned daemon unexpectedly exits.
	consoleBinary := filepath.Join(root, "output", "console", "console")
	daemonBinary := filepath.Join(root, "output", "daemon", "consoled")
	// An allowlist keeps host login, CLI, proxy, shell and configuration variables out.
	env := []string{
		"HOME=" + filepath.Join(root, "user"), "PATH=" + bin, "TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"CODEX_HOME=" + filepath.Join(root, "codex"), "CLAUDE_CONFIG_DIR=" + filepath.Join(root, "claude"),
		"BATUTA_SKILLS=" + filepath.Join(root, "skills", "batuta"), "BATUTA_LANG=en", "LANG=C", "TERM=xterm-256color",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	}
	for _, dir := range []string{bin, filepath.Join(root, "user"), workspace, filepath.Dir(consoleBinary), filepath.Dir(daemonBinary)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	buildEnv := append(append([]string{}, env...), "GOROOT="+runtime.GOROOT(), "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=sum.golang.org")
	// Only the dependency cache location is inherited; the build cache is owned.
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		cache := exec.CommandContext(ctx, goTool, "env", "GOMODCACHE")
		cache.Env = []string{"HOME=" + os.Getenv("HOME"), "GOPATH=" + os.Getenv("GOPATH"), "GOROOT=" + runtime.GOROOT(), "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local"}
		raw, err := cache.Output()
		if err != nil {
			t.Fatalf("module cache location: %v", err)
		}
		moduleCache = strings.TrimSpace(string(raw))
	}
	buildEnv = append(buildEnv, "GOMODCACHE="+moduleCache, "GOCACHE="+filepath.Join(root, "go-cache"))
	for _, build := range []struct{ name, output string }{{"console", consoleBinary}, {"consoled", daemonBinary}} {
		cmd := exec.CommandContext(ctx, goTool, "build", "-o", build.output, "./cmd/"+build.name)
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = buildEnv
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = 5 * time.Second
		t.Logf("BUILD dir=%s argv=%q", cmd.Dir, cmd.Args)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fresh build: %v\n%s", err, raw)
		}
		raw, err := os.ReadFile(build.output)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("BUILD exit=0 binary=%s sha256=%x", build.output, sha256.Sum256(raw))
	}
	marker := filepath.Join(root, "unexpected-execution")
	for provider, version := range map[string]string{"codex": "codex-cli 0.160.0", "claude": "2.1.289 (Claude Code)"} {
		write(filepath.Join(bin, provider), "#!/bin/sh\nif [ \"$#\" = 1 ] && [ \"$1\" = --version ]; then printf '%s\\n' '"+version+"'; exit 0; fi\nprintf '%s\\n' unexpected >> '"+strings.ReplaceAll(marker, "'", "'\\''")+"'\nexit 91\n")
	}
	const planPath = ".batuta/plans/alpha.md"
	const source = "# Plan — Alpha\n**Goal:** Preserve exact outer PTY bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n"
	write(filepath.Join(workspace, planPath), source)
	write(filepath.Join(workspace, ".batuta/profile.md"), "# Profile\nStack: text\nMethodology: TDD\nTest: git diff --check\nBuild: none\nInstall: none\nExecution: sequential\nWorktree: always\nTemplate: templates/generic.md\n")
	write(filepath.Join(workspace, ".batuta/routing.md"), "| Lane | Domain | Executor | Model |\n|---|---|---|---|\n| low | * | codex | fixture |\n| medium | * | codex | fixture |\n| high | * | claude | sonnet |\n| critical | * | self | session |\n")
	write(filepath.Join(workspace, "WORK.md"), "# Work\n\n## In progress\n\n## Done\n")
	// Reserve only an owned loopback address; a collision fails closed at startup.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("environment: owned loopback listener: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	startDaemon := func() *outerJourneyProcess {
		t.Helper()
		cmd := exec.Command(daemonBinary, "-home", home, "-listen", address, "-core", filepath.Join(bin, "absent-core"))
		cmd.Env, cmd.Dir = env, workspace
		p := outerJourneyStart(t, cmd, false)
		processes = append(processes, p)
		return p
	}
	daemon := startDaemon()
	api := client.New("http://"+address, "")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	api.HTTP = &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe := client.New("http://"+address, "")
	probe.HTTP = &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ready := func() {
		t.Helper()
		outerJourneyAwait(t, ctx, "authenticated public daemon readiness", func() bool {
			select {
			case <-daemon.done:
				t.Fatalf("daemon exited before readiness: %v\n%s", daemon.err, daemon.output())
			default:
			}
			token, err := os.ReadFile(filepath.Join(home, "auth.secret"))
			if err != nil {
				return false
			}
			api.Token = strings.TrimSpace(string(token))
			probe.Token = api.Token
			return probe.Probe() == client.StatusReady
		})
		t.Logf("READY authenticated GET /v0/capabilities pid=%d", daemon.cmd.Process.Pid)
	}
	ready()
	ws, err := api.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "Outer PTY", Root: workspace})
	if err != nil {
		t.Fatal(err)
	}
	var accounts []domain.Account
	for _, provider := range []string{"codex", "claude"} {
		a, err := api.CreateAccount(ctx, client.CreateAccountRequest{Label: "Disposable " + provider, Adapter: provider})
		if err != nil {
			t.Fatal(err)
		}
		a, err = api.ConnectAccount(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, a)
	}
	squad, err := api.SaveSquad(ctx, domain.Squad{Name: "Outer squad", Members: []domain.Member{
		{Role: "lead", CLI: "codex", Model: "fixture", AccountID: accounts[0].ID, Maestro: true},
		{Role: "review", CLI: "claude", Model: "sonnet", AccountID: accounts[1].ID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var missions []domain.Mission
	for _, title := range []string{"Other mission", "Selected mission"} {
		m, err := api.CreateMission(ctx, client.CreateMissionRequest{WorkspaceID: ws.ID, Title: title, Brief: "Exact " + title, SquadID: squad.ID})
		if err != nil {
			t.Fatal(err)
		}
		missions = append(missions, m)
	}
	other, selected := missions[0], missions[1]
	if other.ID == selected.ID || other.ID == "" || selected.ID == "" {
		t.Fatal("two distinct mission identities required")
	}
	cmd := exec.Command(consoleBinary, "-home", home, "-listen", address)
	cmd.Env, cmd.Dir = env, workspace
	console := outerJourneyStart(t, cmd, true)
	processes = append(processes, console)
	console.contains(t, ctx, "w workspaces (select / create / setup)")
	selectMission := func(want domain.Mission) {
		t.Helper()
		console.key(t, "m")
		s := console.screen(t, ctx, "mission list", func(s string) bool {
			return strings.Contains(s, "OBJECTIVE") && strings.Contains(s, other.ID) && strings.Contains(s, selected.ID)
		})
		marked := func(s string) bool {
			for _, line := range strings.Split(s, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "> ") && strings.Contains(line, want.ID) {
					return true
				}
			}
			return false
		}
		if !marked(s) {
			console.key(t, "\x1b[B")
			console.screen(t, ctx, "keyboard-selected "+want.ID, marked)
		}
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
	detail, err := api.PlanDetail(ctx, selected.ID, ws.ID)
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
	outerJourneyReviewSource(t, ctx, console, detail)
	console.key(t, "y")
	console.contains(t, ctx, "Confirm: Approve all inspected original source bytes")
	console.key(t, "\r")
	console.screen(t, ctx, "approved selected mission", func(s string) bool {
		return strings.Contains(s, "Immutable approved snapshot") && !strings.Contains(s, "Mission request pending") && !strings.Contains(s, "Confirm:")
	})
	approved, err := api.PlanDetail(ctx, selected.ID, ws.ID)
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
		got, err := api.ListMissions(ctx, ws.ID)
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
		gotReceipt, err := api.PlanApproval(ctx, selected.ID, ws.ID, receipt.ID)
		if err != nil || !reflect.DeepEqual(gotReceipt, receipt) {
			t.Fatalf("immutable approval reread: %+v %v", gotReceipt, err)
		}
		_, err = api.PlanApproval(ctx, other.ID, ws.ID, receipt.ID)
		var missing *client.PlanError
		if !errors.As(err, &missing) || missing.Status != http.StatusNotFound {
			t.Fatalf("other mission approval must be absent: %v", err)
		}
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
				for _, line := range strings.Split(s, "\n") {
					if strings.TrimSpace(line) == "Immutable approved snapshot "+receipt.ID+" "+receipt.Identity.Version {
						return true
					}
				}
				return false
			}
			return !strings.Contains(s, "Immutable approved snapshot")
		})
	}
	refresh(selected)
	console.key(t, "e")
	console.contains(t, ctx, "Confirm: Start native execution from this receipt")
	console.key(t, "\r")
	console.screen(t, ctx, "visible fail-closed authentication", func(s string) bool {
		return strings.Contains(s, "credential_missing") && strings.Contains(s, "Request failed") && !strings.Contains(s, "Mission request pending")
	})
	checkRefusal := func() {
		t.Helper()
		outerJourneyAwait(t, ctx, "native job retirement", func() bool {
			state, err := api.ExecutionState(ctx, selected.ID, ws.ID, receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Target != receipt.Identity || state.ApprovalID != receipt.ID || state.Bound || state.OpenedDigest != "" || state.Watch != nil {
				t.Fatalf("native execution occurred: %+v", state)
			}
			return !state.Active
		})
		sessions, err := api.ListSessions(ctx)
		if err != nil || len(sessions) != 0 {
			t.Fatalf("unexpected sessions: %+v %v", sessions, err)
		}
		gotAccounts, err := api.ListAccounts(ctx)
		if err != nil || len(gotAccounts) != 2 {
			t.Fatalf("account reread: %+v %v", gotAccounts, err)
		}
		for _, a := range gotAccounts {
			matched := false
			for _, original := range accounts {
				if a.ID == original.ID && a.Adapter == original.Adapter && a.VaultRef == filepath.Join(home, "accounts", original.ID) && a.Connected {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("account identity/vault changed: %+v", a)
			}
			if a.Readiness == nil || a.Authed || a.Readiness.Ready || a.Readiness.Authenticated || a.Readiness.CredentialConfigured || !a.Readiness.CLICompatible || a.Readiness.Code != "credential_missing" {
				t.Fatalf("not the absent-credential refusal: %+v", a)
			}
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("model/login command marker: %v", err)
		}
		journal, err := os.ReadDir(filepath.Join(workspace, ".batuta", "journal"))
		if !errors.Is(err, os.ErrNotExist) && (err != nil || len(journal) != 0) {
			t.Fatalf("unexpected delivery journal: %v %v", journal, err)
		}
		checkCatalog()
	}
	checkRefusal()
	refresh(selected)
	events, err := api.Activity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldPID := daemon.cmd.Process.Pid
	daemon.stop(t)
	console.key(t, "f")
	console.contains(t, ctx, "Request failed; confirmed facts and draft retained")
	daemon = startDaemon()
	ready()
	if daemon.cmd.Process.Pid == oldPID {
		t.Fatal("restart did not obtain a distinct retained process handle")
	}
	refresh(selected)
	console.contains(t, ctx, receipt.ID)
	selectMission(other)
	refresh(other)
	selectMission(selected)
	refresh(selected)
	console.contains(t, ctx, receipt.ID)
	checkRefusal()
	afterEvents, err := api.Activity(ctx)
	if err != nil || !reflect.DeepEqual(afterEvents, events) {
		t.Fatalf("refresh/restart repeated a mutation: before=%+v after=%+v err=%v", events, afterEvents, err)
	}
	console.key(t, "q")
	console.join(t)
	daemon.stop(t)
}

// All subprocess authority originates at Start; no PID discovery or API substitutes.
type outerJourneyProcess struct {
	cmd        *exec.Cmd
	pty        *os.File
	done       chan struct{}
	err        error
	mu         sync.Mutex
	raw        bytes.Buffer
	readErr    error
	view       string
	changed    chan struct{}
	readerDone chan struct{}
	joined     bool
}

func outerJourneyStart(t *testing.T, cmd *exec.Cmd, interactive bool) *outerJourneyProcess {
	t.Helper()
	p := &outerJourneyProcess{cmd: cmd, done: make(chan struct{}), changed: make(chan struct{}, 1), readerDone: make(chan struct{})}
	var reader io.ReadCloser
	if interactive {
		master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 80, Cols: 240})
		if err != nil {
			t.Fatalf("environment: outer PTY startup: %v", err)
		}
		p.pty, reader = master, master
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Start(); err != nil {
			r.Close()
			w.Close()
			t.Fatal(err)
		}
		w.Close()
		reader = r
	}
	t.Logf("PROCESS pid=%d argv=%q outerPTY=%t size=240x80", cmd.Process.Pid, cmd.Args, interactive)
	go func() { p.err = cmd.Wait(); close(p.done) }()
	go func() {
		defer close(p.readerDone)
		screen := terminalengine.NewScreen(80, 240)
		screen.SetScrollbackLimit(0)
		parser := terminalengine.NewParser(screen)
		buf := make([]byte, 32768)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				p.mu.Lock()
				if p.raw.Len()+n <= 4<<20 {
					p.raw.Write(buf[:n])
				} else {
					p.readErr = errors.New("outer process exceeded 4 MiB output bound")
				}
				if interactive {
					parser.Feed(buf[:n])
					p.view = ansi.Strip(screen.Render())
				}
				p.mu.Unlock()
				select {
				case p.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
					p.mu.Lock()
					p.readErr = err
					p.mu.Unlock()
				}
				return
			}
		}
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			p.stop(t)
		}
		reader.Close()
		select {
		case <-p.readerDone:
			select {
			case <-p.done:
				p.joined = true
			default:
			}
			p.mu.Lock()
			if p.readErr != nil {
				t.Errorf("owned output reader: %v", p.readErr)
			}
			p.mu.Unlock()
		case <-time.After(5 * time.Second):
			t.Error("owned output reader failed to join")
		}
	})
	return p
}

func (p *outerJourneyProcess) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.raw.String()
}

func (p *outerJourneyProcess) key(t *testing.T, keys string) {
	t.Helper()
	t.Logf("KEY pid=%d bytes=%q", p.cmd.Process.Pid, keys)
	if _, err := io.WriteString(p.pty, keys); err != nil {
		t.Fatal(err)
	}
}

func (p *outerJourneyProcess) screen(t *testing.T, ctx context.Context, label string, matches func(string) bool) string {
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
			t.Fatalf("Console exited before %s: %v\n%s", label, p.err, p.output())
		case <-deadline.C:
			t.Fatalf("screen deadline %s\n%s", label, view)
		case <-ctx.Done():
			t.Fatalf("journey deadline %s: %v\n%s", label, ctx.Err(), view)
		}
	}
}

func (p *outerJourneyProcess) contains(t *testing.T, ctx context.Context, text string) string {
	t.Helper()
	return p.screen(t, ctx, text, func(s string) bool { return strings.Contains(s, text) })
}

func (p *outerJourneyProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("owned SIGTERM: %v", err)
	}
	p.join(t)
}

func (p *outerJourneyProcess) join(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(8 * time.Second):
		t.Errorf("owned pid=%d exceeded graceful exit deadline", p.cmd.Process.Pid)
		if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("owned group termination: %v", err)
		}
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Error("owned process did not join after SIGKILL")
			return
		}
	}
	if p.err != nil || !p.cmd.ProcessState.Success() {
		t.Errorf("owned pid=%d nonzero/signal exit: %v\n%s", p.cmd.Process.Pid, p.err, p.output())
	}
	t.Logf("EXIT pid=%d status=%s", p.cmd.Process.Pid, p.cmd.ProcessState)
}

func outerJourneyAwait(t *testing.T, ctx context.Context, label string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for !ready() {
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatalf("state deadline: %s", label)
		case <-ctx.Done():
			t.Fatalf("journey deadline: %s: %v", label, ctx.Err())
		}
	}
}

func outerJourneyReviewSource(t *testing.T, ctx context.Context, p *outerJourneyProcess, detail domain.PlanDetail) {
	t.Helper()
	page := regexp.MustCompile(`(?m)^\s*(\d+)/(\d+)\s*$`)
	byteLine := regexp.MustCompile(`^([0-9a-f]{8}) (".*")$`)
	var metadata, source []byte
	inSource, previous := false, 0
	for {
		s := p.screen(t, ctx, "next exact source page", func(s string) bool {
			m := page.FindStringSubmatch(s)
			if len(m) == 0 || !strings.Contains(s, "Current source "+detail.Identity.Version) {
				return false
			}
			n, _ := strconv.Atoi(m[1])
			return n > previous && !strings.Contains(s, "Mission request pending")
		})
		m := page.FindStringSubmatch(s)
		previous, _ = strconv.Atoi(m[1])
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "Original source bytes; offsets and lossless ASCII escapes" {
				inSource = true
			}
			parts := byteLine.FindStringSubmatch(line)
			if len(parts) == 0 {
				continue
			}
			offset, err := strconv.ParseInt(parts[1], 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := strconv.Unquote(parts[2])
			if err != nil {
				t.Fatal(err)
			}
			target := &metadata
			if inSource {
				target = &source
			}
			if offset != int64(len(*target)) {
				t.Fatalf("visible byte gap: offset=%d collected=%d", offset, len(*target))
			}
			*target = append(*target, decoded...)
		}
		if strings.Contains(s, "END OF SOURCE") {
			break
		}
		p.key(t, "\x1b[B")
	}
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
