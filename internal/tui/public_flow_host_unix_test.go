//go:build darwin || linux

package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/domain"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/console/internal/webui"
	"github.com/batuta-ai/core/loop"
)

// The pipe-only entrypoint is deliberately absent from the ordinary test list.
// Its success means fixture shutdown, never qualification of a consumer.
func TestMain(m *testing.M) {
	if os.Getenv("BATUTA_TEST_PUBLIC_FLOW_CONFIG") != "" || os.Getenv("BATUTA_TEST_PUBLIC_FLOW_PREPARE") != "" {
		entry := publicFlowServe
		if os.Getenv("BATUTA_TEST_PUBLIC_FLOW_PREPARE") != "" {
			entry = publicFlowPrepare
		}
		flag.Parse()
		if err := flag.Set("test.run", "^OwnedPublicFlowHost$"); err != nil {
			panic(err)
		}
		if err := flag.Set("test.skip", ""); err != nil {
			panic(err)
		}
		// Main constructs and runs an initialized testing.M, including CPU lists,
		// deadlines and cleanups. Standalone RunTests does not initialize these.
		// The private list is never part of the generated ordinary test list.
		testing.Main(regexp.MatchString, []testing.InternalTest{{Name: "OwnedPublicFlowHost", F: entry}}, nil, nil)
		return
	}
	os.Exit(m.Run())
}

type publicFlowConfig struct {
	Home, Core, Helper, Provider, Bin, Skills              string
	CoreSHA256, HelperSHA256, SourceRevision, SourceDigest string
	Web                                                    bool
	ObserveStops                                           bool `json:",omitempty"`
}

type publicFlowControl struct {
	Op, AccountID, Delivery, Phase string
	Ask                            bool
	Approval                       domain.PlanApproval
	Births                         []publicFlowBirth
}
type publicFlowEvent struct {
	Kind, Address, Home, Stacks string
	Data                        any `json:",omitempty"`
	PID                         int
	Config                      *publicFlowConfig `json:",omitempty"`
}

func publicFlowCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func publicFlowWrite(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	publicFlowCheck(t, os.MkdirAll(filepath.Dir(path), 0700))
	publicFlowCheck(t, os.WriteFile(path, body, mode))
}
func publicFlowHash(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	publicFlowCheck(t, err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func publicFlowJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	publicFlowCheck(t, err)
	return b
}

func publicFlowVerifyBinary(t *testing.T, path, digest, pkg string) {
	t.Helper()
	if digest == "" || publicFlowHash(t, path) != digest {
		t.Fatal("binary digest mismatch", path)
	}
	info, err := buildinfo.ReadFile(path)
	publicFlowCheck(t, err)
	if info.Path != pkg || info.GoVersion != "go1.27.2" {
		t.Fatal("unexpected build provenance", info)
	}
	modules := append(info.Deps, &info.Main)
	qualified := false
	for _, mod := range modules {
		if mod.Path == "github.com/batuta-ai/core" && mod.Version == "v1.1.0-beta.55" && mod.Sum == "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=" && mod.Replace == nil {
			qualified = true
		}
	}
	if !qualified {
		t.Fatal("binary lacks pinned Core55 module and checksum", path)
	}
}

func publicFlowServe(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("BATUTA_TEST_PUBLIC_FLOW_CONFIG"))
	publicFlowCheck(t, err)
	var cfg publicFlowConfig
	publicFlowCheck(t, json.Unmarshal(raw, &cfg))
	publicFlowVerifyBinary(t, cfg.Core, cfg.CoreSHA256, "github.com/batuta-ai/core/cmd/batuta")
	publicFlowVerifyBinary(t, cfg.Helper, cfg.HelperSHA256, "github.com/batuta-ai/console/cmd/consoled")
	if cfg.SourceRevision == "" || cfg.SourceDigest == "" {
		t.Fatal("missing source receipt")
	}
	control, events := os.NewFile(3, "private-control"), os.NewFile(4, "private-events")
	defer control.Close()
	defer events.Close()
	enc := json.NewEncoder(events)
	paths, err := consoleruntime.Resolve(cfg.Home, "127.0.0.1:0")
	publicFlowCheck(t, err)
	token, err := consoleruntime.LoadOrCreateAuth(paths.Auth)
	publicFlowCheck(t, err)
	info, err := os.Stat(paths.Auth)
	publicFlowCheck(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatal("token is not private")
	}
	st, err := store.Open(paths.DB)
	publicFlowCheck(t, err)
	manager := session.New(paths.Logs)
	svc := service.New(st, manager, nil)
	svc.Home, svc.CoreExecutable, svc.NativeExecutorExecutable = paths.Home, cfg.Core, cfg.Helper
	publicFlowCheck(t, manager.HostError())
	publicFlowCheck(t, svc.ReconcileSessions())
	accounts, err := st.ListAccounts()
	publicFlowCheck(t, err)
	for _, account := range accounts {
		journeyFixtureAuthentication(t, svc, account)
	}
	var web fs.FS
	if cfg.Web {
		web, err = fs.Sub(webui.Dist, "dist")
		publicFlowCheck(t, err)
	}
	handler, stopObserver := publicFlowObserveStops((&api.Server{Svc: svc, Token: token, Web: web}).Handler(), cfg.ObserveStops)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	// Defer runs before testing cleanups, keeping synthetic readiness alive until joins.
	var served chan error
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		nativeErr := svc.ShutdownNative(ctx)
		sessionErr := manager.Shutdown(ctx)
		httpErr := srv.Shutdown(ctx)
		if nativeErr != nil || sessionErr != nil || httpErr != nil {
			t.Errorf("host drain failed; retaining dependencies until process exit: %v", errors.Join(nativeErr, sessionErr, httpErr))
			return
		}
		if served != nil {
			if err := <-served; !errors.Is(err, http.ErrServerClosed) {
				t.Error(err)
			}
		}
		publicFlowCheck(t, st.Close())
	}()
	fixtureControl, releaseFixture := publicFlowBrowserControls(t, cfg)
	// Release held journal descriptors before native/session/HTTP drain, even on EOF or failure.
	defer releaseFixture()
	ln, err := net.Listen("tcp", paths.Listen)
	publicFlowCheck(t, err)
	served = make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	address := ln.Addr().String()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	for _, authorized := range []bool{false, true} {
		req, err := http.NewRequest(http.MethodGet, "http://"+address+"/v0/capabilities", nil)
		publicFlowCheck(t, err)
		want := 401
		if authorized {
			req.Header.Set("Authorization", "Bearer "+token)
			want = 200
		}
		res, err := client.Do(req)
		publicFlowCheck(t, err)
		_, err = io.Copy(io.Discard, res.Body)
		publicFlowCheck(t, errors.Join(err, res.Body.Close()))
		if res.StatusCode != want {
			t.Fatal("host authentication readiness", res.StatusCode, want)
		}
	}
	publicFlowCheck(t, enc.Encode(publicFlowEvent{Kind: "ready", Address: address, Home: cfg.Home, PID: os.Getpid(), Config: &cfg}))
	dec := json.NewDecoder(control)
	for {
		var cmd publicFlowControl
		err := dec.Decode(&cmd)
		if errors.Is(err, io.EOF) {
			return
		}
		publicFlowCheck(t, err)
		switch cmd.Op {
		case "authenticate":
			account, err := st.GetAccount(cmd.AccountID)
			publicFlowCheck(t, err)
			if account.Adapter != cfg.Provider {
				t.Fatal("fixture account provider mismatch")
			}
			// Capacity and synthetic credentials are fixture setup, never approval evidence.
			account.SessionCap = 2
			publicFlowCheck(t, st.PutAccount(account))
			journeyFixtureAuthentication(t, svc, account)
			publicFlowCheck(t, enc.Encode(publicFlowEvent{Kind: "authenticated"}))
		case "stacks":
			buf := make([]byte, 4<<20)
			n := runtime.Stack(buf, true)
			publicFlowCheck(t, enc.Encode(publicFlowEvent{Kind: "stacks", Stacks: string(buf[:n])}))
		case "stop_observations":
			publicFlowCheck(t, enc.Encode(publicFlowEvent{Kind: cmd.Op, Data: stopObserver.snapshot()}))
		case "drain":
			return
		default:
			data, handled := fixtureControl(cmd)
			if !handled {
				t.Fatal("unknown private host command", cmd.Op)
			}
			publicFlowCheck(t, enc.Encode(publicFlowEvent{Kind: cmd.Op, Data: data}))
		}
	}
}

func publicFlowPrepare(t *testing.T) {
	dir := os.Getenv("BATUTA_TEST_PUBLIC_FLOW_PREPARE")
	if !filepath.IsAbs(dir) {
		t.Fatal("prepare requires an absolute, nonexistent owned directory")
	}
	publicFlowCheck(t, os.Mkdir(dir, 0700))
	provider := os.Getenv("BATUTA_TEST_PUBLIC_FLOW_PROVIDER")
	if provider != "codex" && provider != "claude" {
		t.Fatal("select codex or claude")
	}
	cfg := publicFlowBuildAt(t, provider, dir)
	paths, err := consoleruntime.Resolve(cfg.Home, "127.0.0.1:0")
	publicFlowCheck(t, err)
	token, err := consoleruntime.LoadOrCreateAuth(paths.Auth)
	publicFlowCheck(t, err)
	info, err := os.Stat(paths.Auth)
	publicFlowCheck(t, err)
	if token == "" || info.Mode().Perm() != 0600 {
		t.Fatal("prepared authentication is missing or not private")
	}
	publicFlowWrite(t, filepath.Join(dir, "config.json"), publicFlowJSON(t, cfg), 0600)
	t.Log("owned prepare completed: real binaries, provenance, private auth and config written")
}

func publicFlowBuild(t *testing.T, provider string) publicFlowConfig {
	t.Helper()
	return publicFlowBuildAt(t, provider, t.TempDir())
}

func publicFlowBuildAt(t *testing.T, provider, dir string) publicFlowConfig {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	publicFlowCheck(t, err)
	cfg := publicFlowConfig{Home: filepath.Join(dir, "home"), Core: filepath.Join(dir, "batuta"), Helper: filepath.Join(dir, "consoled"), Provider: provider, Bin: filepath.Join(dir, "bin"), Skills: filepath.Join(dir, "skills"), Web: true}
	publicFlowCheck(t, os.MkdirAll(cfg.Home, 0700))
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	run := func(dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(goBin, args...)
		cmd.Dir = dir
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("build %q: %s: %v", args, raw, err)
		}
		return raw
	}
	publicFlowWrite(t, filepath.Join(dir, "core-module.json"), run(root, "list", "-m", "-json", "github.com/batuta-ai/core"), 0600)
	run(root, "build", "-ldflags", "-X main.buildVersion=v1.1.0-beta.55", "-o", cfg.Core, "github.com/batuta-ai/core/cmd/batuta")
	run(root, "build", "-o", cfg.Helper, "./cmd/consoled")
	cfg.CoreSHA256, cfg.HelperSHA256 = publicFlowHash(t, cfg.Core), publicFlowHash(t, cfg.Helper)
	git := exec.Command("git", "rev-parse", "HEAD")
	git.Dir = root
	rev, err := git.Output()
	publicFlowCheck(t, err)
	cfg.SourceRevision = strings.TrimSpace(string(rev))
	git = exec.Command("git", "ls-files", "-z")
	git.Dir = root
	files, err := git.Output()
	publicFlowCheck(t, err)
	digest := sha256.New()
	names := strings.Split(string(files), "\x00")
	for _, fixture := range []string{"internal/tui/public_flow_host_unix_test.go", "internal/tui/public_flow_http_unix_test.go"} {
		found := false
		for _, name := range names {
			if name == fixture {
				found = true
			}
		}
		if !found {
			names = append(names, fixture)
		}
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		publicFlowCheck(t, err)
		fmt.Fprintf(digest, "%s\x00%d\x00", name, len(raw))
		_, _ = digest.Write(raw)
	}
	cfg.SourceDigest = hex.EncodeToString(digest.Sum(nil))
	publicFlowVerifyBinary(t, cfg.Core, cfg.CoreSHA256, "github.com/batuta-ai/core/cmd/batuta")
	publicFlowVerifyBinary(t, cfg.Helper, cfg.HelperSHA256, "github.com/batuta-ai/console/cmd/consoled")
	version := map[string]string{"codex": "codex-cli 0.160.0", "claude": "2.1.289 (Claude Code)"}[provider]
	publicFlowWrite(t, filepath.Join(cfg.Bin, provider), []byte(publicFlowProviderScript(version)), 0700)
	for _, name := range []string{"batuta", "batuta-loop", "batuta-review"} {
		publicFlowWrite(t, filepath.Join(cfg.Skills, name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: disposable public flow fixture\n---\n"), 0600)
	}
	publicFlowWrite(t, filepath.Join(cfg.Skills, "batuta/adapters", provider+".md"), []byte("---\nname: "+provider+"\nexecutable: "+provider+"\nrun: '"+provider+" --model {model} \"{brief}\" < /dev/null'\nreadonly: '"+provider+" --verify --model {model} \"{prompt}\" < /dev/null'\navailable: '"+provider+" --version'\nmodels: '"+provider+" debug models'\nmodel_flags: --model {model}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n"), 0600)
	publicFlowWrite(t, filepath.Join(cfg.Skills, "batuta/templates/generic.md"), []byte("# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n"), 0600)
	publicFlowWrite(t, filepath.Join(dir, "provenance.json"), publicFlowJSON(t, cfg), 0600)
	exe, err := os.Executable()
	publicFlowCheck(t, err)
	publicFlowWrite(t, filepath.Join(dir, "host.sha256"), []byte(publicFlowHash(t, exe)+"\n"), 0600)
	t.Logf("owned host provenance %s host_sha256=%s", publicFlowJSON(t, cfg), publicFlowHash(t, exe))
	return cfg
}

func publicFlowProviderScript(version string) string {
	// Keep the original exact capability/negative branches; ordinary work alone
	// enters the fixture state machine, keyed by canonical Git common directory.
	script := nativeJourneyProviderScript(version)
	script = strings.Replace(script, "--verify) printf 'TASK 1: DONE\\n';exit 0;;", "--verify) printf '<<<FINDINGS\\nFINDINGS>>>\\nTASK 1: DONE\\n';exit 0;;", 1)
	return strings.TrimSuffix(script, "printf 'BATUTA-QUESTION: exact native question?\\n'\n") + `control=$(git rev-parse --path-format=absolute --git-common-dir)/public-flow
[ -d "$control" ] || exit 91
if [ -f "$control/ask" ] && [ ! -f "$control/asked" ]; then
 printf 'asked\n' > "$control/asked"
 printf 'BATUTA-QUESTION: exact native question?\n'
 exit 0
fi
IFS= read -r phase < "$control/phase" || exit 92
case "$phase" in first|replacement) ;; *) exit 93;; esac
[ -p "$control/$phase.release" ] || exit 94
/bin/sleep 300 &
child=$!
printf '%s %s\n' "$$" "$child" > "$control/$phase.birth.tmp"
mv "$control/$phase.birth.tmp" "$control/$phase.birth"
read release < "$control/$phase.release"
[ "$release" = "$phase" ] || exit 95
kill "$child"
wait "$child" 2>/dev/null
printf 'approved output\n' > out.txt
printf 'completed\n'
`
}

type publicFlowHost struct {
	cfg            publicFlowConfig
	cmd            *exec.Cmd
	input, output  *os.File
	enc            *json.Encoder
	dec            *json.Decoder
	address, token string
	done           chan error
	joined         bool
}

func publicFlowLaunch(t *testing.T, cfg publicFlowConfig) *publicFlowHost {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	publicFlowWrite(t, path, publicFlowJSON(t, cfg), 0600)
	childIn, input, err := os.Pipe()
	publicFlowCheck(t, err)
	output, childOut, err := os.Pipe()
	publicFlowCheck(t, err)
	exe, err := os.Executable()
	publicFlowCheck(t, err)
	cmd := exec.Command(exe, "-test.run=^$", "-test.timeout=3m")
	git, err := exec.LookPath("git")
	publicFlowCheck(t, err)
	cmd.Env = []string{"BATUTA_TEST_PUBLIC_FLOW_CONFIG=" + path, "HOME=" + cfg.Home, "PATH=" + cfg.Bin + ":" + filepath.Dir(git) + ":/usr/bin:/bin", "BATUTA_SKILLS=" + filepath.Join(cfg.Skills, "batuta"), "BATUTA_LANG=en", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "TMPDIR=" + os.TempDir()}
	cmd.ExtraFiles = []*os.File{childIn, childOut}
	log, err := os.OpenFile(filepath.Join(filepath.Dir(path), "host.log"), os.O_CREATE|os.O_WRONLY, 0600)
	publicFlowCheck(t, err)
	cmd.Stdout, cmd.Stderr = log, log
	publicFlowCheck(t, cmd.Start())
	publicFlowCheck(t, childIn.Close())
	publicFlowCheck(t, childOut.Close())
	h := &publicFlowHost{cfg: cfg, cmd: cmd, input: input, output: output, enc: json.NewEncoder(input), dec: json.NewDecoder(output), done: make(chan error, 1)}
	go func() { h.done <- cmd.Wait(); _ = log.Close() }()
	t.Cleanup(func() { h.close(t) })
	var event publicFlowEvent
	publicFlowCheck(t, output.SetReadDeadline(time.Now().Add(30*time.Second)))
	if err := h.dec.Decode(&event); err != nil {
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "host.log"))
		t.Fatalf("host readiness: %v\n%s", err, raw)
	}
	if event.Kind != "ready" || event.PID != cmd.Process.Pid || event.Config == nil || *event.Config != cfg {
		t.Fatal("host ready identity", event)
	}
	h.address = event.Address
	token, err := os.ReadFile(filepath.Join(cfg.Home, "auth.secret"))
	publicFlowCheck(t, err)
	h.token = strings.TrimSpace(string(token))
	return h
}
func (h *publicFlowHost) control(t *testing.T, cmd publicFlowControl) publicFlowEvent {
	t.Helper()
	publicFlowCheck(t, h.enc.Encode(cmd))
	publicFlowCheck(t, h.output.SetReadDeadline(time.Now().Add(10*time.Second)))
	var event publicFlowEvent
	publicFlowCheck(t, h.dec.Decode(&event))
	return event
}
func (h *publicFlowHost) close(t *testing.T) {
	t.Helper()
	if h.joined {
		return
	}
	h.joined = true
	// Closing the owned control pipe requests drain even after a failing consumer.
	_ = h.input.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Error("owned host exit", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("owned host did not drain; killing only the owned host, qualification failed")
		_ = h.cmd.Process.Kill()
		<-h.done
	}
	_ = h.output.Close()
}

// Browser controls reuse the HTTP fixture's real locks, FIFO protocol and incarnation
// inspection. They never write approval, execution, journal terminal or review records.
func publicFlowBrowserControls(t *testing.T, cfg publicFlowConfig) (func(publicFlowControl) (any, bool), func()) {
	type heldJournal struct {
		file   *os.File
		unlock func()
	}
	held := map[string]heldJournal{}
	var foreign []*exec.Cmd
	cleanup := func() {
		for key, lock := range held {
			lock.unlock()
			delete(held, key)
		}
		for _, cmd := range foreign {
			publicFlowCheck(t, cmd.Process.Kill())
			err := cmd.Wait()
			var exited *exec.ExitError
			if !errors.As(err, &exited) {
				publicFlowCheck(t, err)
			}
		}
		foreign = nil
	}
	control := func(cmd publicFlowControl) (any, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a := cmd.Approval
		key := a.Identity.Binding.CanonicalRoot + "\x00" + cmd.Delivery
		switch cmd.Op {
		case "workspace":
			root := publicFlowWorkspace(t, cfg, cmd.Ask)
			destination := filepath.Join(cfg.Home, "browser-roots", filepath.Base(filepath.Dir(root))+"-"+filepath.Base(root))
			publicFlowCheck(t, os.MkdirAll(filepath.Dir(destination), 0700))
			publicFlowCheck(t, os.Rename(root, destination))
			return map[string]string{"root": destination, "plan": publicFlowPlan, "path": publicFlowPlanPath}, true
		case "foreign":
			child := exec.Command("/bin/sleep", "300")
			publicFlowCheck(t, child.Start())
			foreign = append(foreign, child)
			return publicFlowObserve(t, child.Process.Pid), true
		case "tree":
			return publicFlowTree(t, ctx, a, cmd.Phase), true
		case "alive":
			publicFlowAlive(t, cmd.Births...)
			return cmd.Births, true
		case "retired":
			publicFlowRetired(t, ctx, cmd.Births)
			return cmd.Births, true
		case "hold":
			if _, ok := held[key]; ok {
				t.Fatal("journal already held")
			}
			f, unlock := publicFlowLock(t, a, cmd.Delivery)
			held[key] = heldJournal{f, unlock}
			return map[string]bool{"exclusive": true, "reader_blocked": true}, true
		case "unlock", "corrupt":
			lock, ok := held[key]
			if !ok {
				t.Fatal("journal not held")
			}
			if cmd.Op == "corrupt" {
				// Same-inode corruption is allowed only after the exact target has retired.
				if len(cmd.Births) != 2 {
					t.Fatal("fault requires original provider tree")
				}
				publicFlowRetired(t, ctx, cmd.Births)
				before, err := lock.file.Stat()
				publicFlowCheck(t, err)
				_, err = lock.file.WriteString("not-a-journal-record\n")
				publicFlowCheck(t, err)
				publicFlowCheck(t, lock.file.Sync())
				after, err := os.Stat(lock.file.Name())
				publicFlowCheck(t, err)
				if !os.SameFile(before, after) {
					t.Fatal("fault replaced journal inode")
				}
			}
			lock.unlock()
			delete(held, key)
			return map[string]bool{"released": true, "same_inode": cmd.Op == "corrupt"}, true
		case "release":
			publicFlowRelease(t, a, cmd.Phase)
			return cmd.Phase, true
		case "phase":
			if cmd.Phase != "replacement" {
				t.Fatal("invalid replacement phase")
			}
			publicFlowWrite(t, filepath.Join(publicFlowControlDir(a), "phase"), []byte(cmd.Phase+"\n"), 0600)
			return cmd.Phase, true
		case "journal":
			if _, ok := held[key]; ok {
				t.Fatal("cannot read held journal")
			}
			return publicFlowJournal(t, a, cmd.Delivery), true
		case "ship":
			opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: cmd.Delivery}
			observation, err := loop.ObserveSupervision(opts)
			if err != nil {
				return map[string]bool{"ready": false}, true
			}
			gate, err := loop.CheckSupervisionGate(ctx, opts)
			if err != nil || observation.Review == nil || observation.Review.State != "reported" || !gate.Required || !gate.Cleared || gate.EvidenceDigest == "" {
				return map[string]bool{"ready": false}, true
			}
			if observation.Review.Outcome != "SHIP" || observation.Review.ApprovalReceiptID != a.ID {
				t.Fatal("SHIP receipt mismatch")
			}
			raw, err := os.ReadFile(filepath.Join(a.Identity.Binding.CanonicalRoot, "out.txt"))
			publicFlowCheck(t, err)
			if string(raw) != "approved output\n" {
				t.Fatal("canonical output mismatch")
			}
			return map[string]any{"ready": true, "review": observation.Review, "gate": gate, "output": string(raw)}, true
		default:
			return nil, false
		}
	}
	return control, cleanup
}

// The opt-in observer records only Stop requests consumed by the original API.
// No goroutine/channel is added: HTTP Shutdown joins handlers before Store.Close,
// and the existing owned control pipe carries synchronous snapshots.
const publicFlowStopBodyLimit = 16 << 10
const publicFlowStopRecordLimit = 32

type publicFlowStopObservation struct {
	Sequence   int                          `json:"sequence"`
	Path       string                       `json:"path"`
	Request    *domain.ExecutionStopRequest `json:"request,omitempty"`
	Status     int                          `json:"status"`
	BodyBytes  int                          `json:"body_bytes"`
	BodySHA256 string                       `json:"body_sha256"`
	Truncated  bool                         `json:"truncated"`
}

type publicFlowStopSnapshot struct {
	Enabled  bool                        `json:"enabled"`
	Count    int                         `json:"count"`
	InFlight int                         `json:"in_flight"`
	Overflow bool                        `json:"overflow"`
	Records  []publicFlowStopObservation `json:"records"`
}

type publicFlowStopObserver struct {
	mu    sync.Mutex
	state publicFlowStopSnapshot
}

func (o *publicFlowStopObserver) snapshot() publicFlowStopSnapshot {
	if o == nil {
		return publicFlowStopSnapshot{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.state
	s.Records = append([]publicFlowStopObservation(nil), s.Records...)
	return s
}

type publicFlowStopBody struct {
	io.ReadCloser
	captured []byte
	consumed int
}

func (b *publicFlowStopBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.consumed += n
	remaining := publicFlowStopBodyLimit - len(b.captured)
	b.captured = append(b.captured, p[:min(n, remaining)]...)
	return n, err
}

type publicFlowStopWriter struct {
	http.ResponseWriter
	status int
}

// Only the JSON Stop route is wrapped. All other routes (including streaming
// and upgrades) retain their original writer; ResponseController can unwrap it.
func (w *publicFlowStopWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *publicFlowStopWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *publicFlowStopWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func publicFlowObserveStops(next http.Handler, enabled bool) (http.Handler, *publicFlowStopObserver) {
	if !enabled {
		return next, nil
	}
	o := &publicFlowStopObserver{state: publicFlowStopSnapshot{Enabled: true}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if r.Method != http.MethodPost || len(parts) != 6 || parts[1] != "v0" || parts[2] != "missions" || parts[3] == "" || parts[4] != "execution" || parts[5] != "stop" {
			next.ServeHTTP(w, r)
			return
		}
		o.mu.Lock()
		o.state.Count++
		sequence := o.state.Count
		o.state.InFlight++
		o.mu.Unlock()
		body := &publicFlowStopBody{ReadCloser: r.Body}
		r.Body = body
		writer := &publicFlowStopWriter{ResponseWriter: w}
		// Tee only bytes requested by the original handler, never pre-read a body or
		// buffer a response. Do not retain headers or arbitrary request body text.
		defer func() {
			sum := sha256.Sum256(body.captured)
			record := publicFlowStopObservation{Sequence: sequence, Path: r.URL.Path, Status: writer.status, BodyBytes: body.consumed, BodySHA256: hex.EncodeToString(sum[:]), Truncated: body.consumed > publicFlowStopBodyLimit}
			var request domain.ExecutionStopRequest
			decoder := json.NewDecoder(bytes.NewReader(body.captured))
			decoder.DisallowUnknownFields()
			if !record.Truncated && decoder.Decode(&request) == nil && decoder.Decode(new(json.RawMessage)) == io.EOF && request.Validate() == nil {
				record.Request = &request
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			o.state.InFlight--
			if len(o.state.Records) < publicFlowStopRecordLimit {
				o.state.Records = append(o.state.Records, record)
			} else {
				o.state.Overflow = true
			}
		}()
		next.ServeHTTP(writer, r)
	}), o
}
