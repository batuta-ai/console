//go:build windows

package tui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"
	"unsafe"

	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"golang.org/x/sys/windows"
)

func TestWindowsPublicFlowSourceProvenance(t *testing.T) {
	root := winFlowFreshRoot(t)
	source := filepath.Join(root, "source")
	manifest := map[string]string{}
	for _, name := range []string{"go.mod", "go.sum", "internal/tui/public_flow_host_windows_test.go", "internal/tui/public_flow_native_windows_test.go", "internal/extra.go"} {
		data := []byte("owned source: " + name)
		winFlowWrite(t, filepath.Join(source, filepath.FromSlash(name)), data)
		manifest[name] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	caller := filepath.Join(source, "internal", "tui", "public_flow_host_windows_test.go")
	head := strings.Repeat("a", 40)
	path := filepath.Join(root, "manifest.json")
	write := func(m map[string]string) { winFlowWrite(t, path, winFlowJSON(t, m)) }
	write(manifest)
	validate := func(revision, manifestPath, callerPath string) error {
		_, err := winFlowSource(source, callerPath, revision, manifestPath, "", nil)
		return err
	}
	winFlowCheck(t, validate(head, path, caller))
	for _, tc := range []struct{ name, revision, path, caller string }{
		{"missing-head", "", path, caller}, {"short-head", "abc", path, caller},
		{"nonhex-head", strings.Repeat("g", 40), path, caller},
		{"missing-manifest", head, "", caller}, {"absent-manifest", head, path + ".absent", caller},
		{"outside-caller", head, path, filepath.Join(root, "public_flow_host_windows_test.go")},
		{"other-caller", head, path, filepath.Join(source, "internal", "extra.go")},
		{"relative-manifest", head, "manifest.json", caller},
		{"unclean-manifest", head, root + string(os.PathSeparator) + "." + string(os.PathSeparator) + "manifest.json", caller},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validate(tc.revision, tc.path, tc.caller); err == nil {
				t.Fatal("invalid source provenance accepted")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
	}{
		{"omitted-file", func(m map[string]string) { delete(m, "internal/extra.go") }},
		{"omitted-host", func(m map[string]string) { delete(m, "internal/tui/public_flow_host_windows_test.go") }},
		{"omitted-provider", func(m map[string]string) { delete(m, "internal/tui/public_flow_native_windows_test.go") }},
		{"missing-file", func(m map[string]string) { m["missing.go"] = strings.Repeat("0", 64) }},
		{"mismatch", func(m map[string]string) { m["go.mod"] = strings.Repeat("0", 64) }},
		{"invalid-hash", func(m map[string]string) { m["go.mod"] = "sha256" }},
		{"traversal", func(m map[string]string) { m["../outside.go"] = m["go.mod"] }},
		{"noncanonical", func(m map[string]string) { m["internal/../go.mod"] = m["go.mod"] }},
		{"backslash", func(m map[string]string) { m[`internal\extra.go`] = m["go.mod"] }},
		{"absolute", func(m map[string]string) { m["C:/outside.go"] = m["go.mod"] }},
		{"case-alias", func(m map[string]string) { m["GO.MOD"] = m["go.mod"] }},
		{"alternate-stream", func(m map[string]string) { m["go.mod:stream"] = m["go.mod"] }},
		{"reserved-name", func(m map[string]string) { m["NUL"] = m["go.mod"] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := make(map[string]string, len(manifest))
			for k, v := range manifest {
				m[k] = v
			}
			tc.change(m)
			write(m)
			if err := validate(head, path, caller); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	valid := string(winFlowJSON(t, manifest))
	duplicate := strings.TrimSuffix(valid, "}") + `,"go.mod":"` + manifest["go.mod"] + `"}`
	for _, raw := range []string{`null`, `{}`, duplicate, valid + " {}", strings.Repeat(" ", winFlowLimit+1)} {
		winFlowWrite(t, path, []byte(raw))
		if err := validate(head, path, caller); err == nil {
			t.Fatal("malformed manifest accepted")
		}
	}
	write(manifest)
	inside := filepath.Join(source, "manifest.json")
	winFlowWrite(t, inside, winFlowJSON(t, manifest))
	if err := validate(head, inside, caller); err == nil {
		t.Fatal("internal manifest accepted")
	}
	winFlowCheck(t, os.Remove(inside))
	winFlowWrite(t, filepath.Join(source, "unlisted.go"), []byte("unlisted"))
	if err := validate(head, path, caller); err == nil {
		t.Fatal("unlisted source accepted")
	}
	winFlowCheck(t, os.Remove(filepath.Join(source, "unlisted.go")))
	winFlowWrite(t, filepath.Join(source, ".git"), []byte("not a repository"))
	if err := validate(head, path, caller); err == nil {
		t.Fatal("archive bypassed Git identity")
	}
	winFlowCheck(t, os.Remove(filepath.Join(source, ".git")))
	winFlowCheck(t, validate(head, path, caller))
	winFlowWrite(t, caller, []byte("changed after manifest"))
	if err := validate(head, path, caller); err == nil {
		t.Fatal("changed runtime.Caller source accepted")
	}
}

func TestWindowsPublicFlowArchiveManifestCauses(t *testing.T) {
	root := winFlowFreshRoot(t)
	source := filepath.Join(root, "source")
	manifest := map[string]string{}
	for _, name := range []string{"go.mod", "go.sum", "internal/tui/public_flow_host_windows_test.go", "internal/tui/public_flow_native_windows_test.go"} {
		raw := []byte("owned source " + name)
		winFlowWrite(t, filepath.Join(source, filepath.FromSlash(name)), raw)
		manifest[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	caller := filepath.Join(source, "internal", "tui", "public_flow_host_windows_test.go")
	path := filepath.Join(root, "manifest.json")
	valid := string(winFlowJSON(t, manifest))
	for _, tc := range []struct{ name, raw, want string }{
		{"hash", `{"go.mod":"invalid"}`, "invalid SHA256 for go.mod"},
		{"duplicate", `{"go.mod":"` + manifest["go.mod"] + `","go.mod":"` + manifest["go.mod"] + `"}`, "duplicate/aliased manifest path"},
		{"trailing", valid + " {}", "trailing source manifest data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			winFlowWrite(t, path, []byte(tc.raw))
			_, err := winFlowSource(source, caller, strings.Repeat("a", 40), path, "", nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("manifest cause = %v, want %q", err, tc.want)
			}
		})
	}
	_, err := winFlowSource(source, caller, strings.Repeat("a", 40), path+".missing", "", nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost missing-manifest cause: %v", err)
	}
}

func TestWindowsPublicFlowGitGeneratedArtifacts(t *testing.T) {
	cfg := winFlowFixture(t, "codex", false)
	source := filepath.Join(cfg.Root, "source")
	for _, name := range []string{"go.mod", "go.sum", "internal/tui/public_flow_host_windows_test.go", "internal/tui/public_flow_native_windows_test.go"} {
		winFlowWrite(t, filepath.Join(source, filepath.FromSlash(name)), []byte("owned source "+name+"\n"))
	}
	winFlowWrite(t, filepath.Join(source, ".gitignore"), []byte(".batuta/runs/\nweb/node_modules/\n"))
	winFlowGit(t, cfg, source, "init")
	winFlowGit(t, cfg, source, "config", "user.name", "Owned fixture")
	winFlowGit(t, cfg, source, "config", "user.email", "fixture@example.invalid")
	winFlowGit(t, cfg, source, "config", "core.autocrlf", "false")
	winFlowGit(t, cfg, source, "add", ".")
	winFlowGit(t, cfg, source, "commit", "-m", "owned source")
	caller := filepath.Join(source, "internal", "tui", "public_flow_host_windows_test.go")
	validate := func() (winFlowSourceProof, error) {
		return winFlowSource(source, caller, "", "", cfg.Git, winFlowEnvironment(cfg))
	}
	before, err := validate()
	winFlowCheck(t, err)
	for _, name := range []string{".batuta/runs/owned-proof.json", "web/node_modules/owned/cache.txt"} {
		winFlowWrite(t, filepath.Join(source, filepath.FromSlash(name)), []byte("generated artifact"))
	}
	after, err := validate()
	if err != nil {
		t.Fatalf("ignored generated artifacts rejected: %v", err)
	}
	if before.Revision != after.Revision || before.Digest != after.Digest || !reflect.DeepEqual(before.Files, after.Files) {
		t.Fatal("artifacts changed tracked source provenance")
	}
	winFlowWrite(t, filepath.Join(source, "internal", "unlisted.go"), []byte("untracked Go input"))
	if _, err := validate(); err == nil {
		t.Fatal("untracked Go source accepted")
	}
	winFlowCheck(t, os.Remove(filepath.Join(source, "internal", "unlisted.go")))
	winFlowGit(t, cfg, source, "config", "core.autocrlf", "true")
	// Clean Git filters must not authorize working bytes different from HEAD.
	winFlowWrite(t, filepath.Join(source, "go.mod"), []byte("owned source go.mod\r\n"))
	if _, err := validate(); err == nil {
		t.Fatal("normalized CRLF bytes accepted as exact source")
	}
}

func TestWindowsPublicFlowBinaryProvenance(t *testing.T) {
	exe, err := os.Executable()
	winFlowCheck(t, err)
	info, err := buildinfo.ReadFile(exe)
	winFlowCheck(t, err)
	// These controls validate the actual binary metadata, then alter one identity.
	race := winFlowBinaryRace(info)
	winFlowCheck(t, winFlowBuildIdentity(info, "github.com/batuta-ai/console/internal/tui.test", race))
	setting := func(i *debug.BuildInfo, key, value string) {
		for n := range i.Settings {
			if i.Settings[n].Key == key {
				i.Settings[n].Value = value
				return
			}
		}
		i.Settings = append(i.Settings, debug.BuildSetting{Key: key, Value: value})
	}
	for _, tc := range []struct {
		name   string
		change func(*debug.BuildInfo)
	}{
		{"toolchain", func(i *debug.BuildInfo) { i.GoVersion = "go0.0" }},
		{"package", func(i *debug.BuildInfo) { i.Path = "fake.test" }},
		{"module", func(i *debug.BuildInfo) { i.Main.Path = "fake/module" }},
		{"missing-core", func(i *debug.BuildInfo) { i.Deps = nil }},
		{"core-sum", func(i *debug.BuildInfo) {
			for _, m := range i.Deps {
				if m.Path == "github.com/batuta-ai/core" {
					m.Sum = "h1:fake"
				}
			}
		}},
		{"core-version", func(i *debug.BuildInfo) {
			for _, m := range i.Deps {
				if m.Path == "github.com/batuta-ai/core" {
					m.Version = "v0.0.0"
				}
			}
		}},
		{"replace", func(i *debug.BuildInfo) { i.Main.Replace = &debug.Module{Path: "local"} }},
		{"core-replace", func(i *debug.BuildInfo) {
			for _, m := range i.Deps {
				if m.Path == "github.com/batuta-ai/core" {
					m.Replace = &debug.Module{Path: "local"}
				}
			}
		}},
		{"race", func(i *debug.BuildInfo) {
			setting(i, "-race", fmt.Sprint(!race))
		}},
		{"os", func(i *debug.BuildInfo) {
			setting(i, "GOOS", "darwin")
		}},
		{"arch", func(i *debug.BuildInfo) {
			setting(i, "GOARCH", "arm64")
		}},
		{"cgo", func(i *debug.BuildInfo) {
			setting(i, "CGO_ENABLED", "invalid")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy, err := buildinfo.ReadFile(exe)
			winFlowCheck(t, err)
			tc.change(copy)
			if winFlowBuildIdentity(copy, "github.com/batuta-ai/console/internal/tui.test", race) == nil {
				t.Fatal("false binary identity accepted")
			}
		})
	}
}

// These are component controls, not SDK Run or SHIP evidence.
func TestWindowsPublicFlowProviderProtocols(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := winFlowFixture(t, provider, false)
			controls := newWinFlowControls(cfg)
			defer controls.close(t)
			root := controls.workspace(t, true, false)
			controls.foreignStart(t)
			cli := filepath.Join(cfg.Bin, provider+".exe")
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.Command(cli, args...)
				cmd.Dir, cmd.Env = root, winFlowEnvironment(cfg)
				out, err := cmd.Output()
				winFlowCheck(t, err)
				return string(out)
			}
			for _, tc := range []struct {
				args []string
				want string
			}{
				{[]string{"--version"}, winFlowVersion(provider) + "\n"},
				{[]string{"debug", "models"}, "{\"models\":[{\"slug\":\"qa-native\"}]}\n"},
				{[]string{"doctor"}, "{}\n"}, {[]string{"plugin", "list"}, "{}\n"},
				{[]string{"--verify", "review"}, "<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE\n"},
			} {
				if got := run(tc.args...); got != tc.want {
					t.Fatalf("protocol %q: %q", tc.args, got)
				}
			}
			sha := winFlowGit(t, cfg, root, "rev-parse", "HEAD")
			if got := run("--model", "qa-native", winFlowCapabilityPrompt); got != "BATUTA-CAPABLE "+sha+"\n" {
				t.Fatal(got)
			}
			outside := exec.Command(cli, "--model", "qa-native", winFlowCapabilityPrompt)
			outside.Dir, outside.Env = cfg.Root, winFlowEnvironment(cfg)
			if out, err := outside.CombinedOutput(); err == nil || bytes.Contains(out, []byte("BATUTA-CAPABLE")) || !bytes.Contains(out, []byte("fatal:")) {
				t.Fatalf("capability hid Git failure: %q %v", out, err)
			}
			// Exact argument shape matters: a lookalike prompt must take ordinary work.
			if got := run("--model", "qa-native", "prefix "+winFlowCapabilityPrompt); got != "BATUTA-QUESTION: exact native question?\n" {
				t.Fatal(got)
			}
			common := winFlowGit(t, cfg, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if _, err := os.Stat(filepath.Join(common, "public-flow", "first.birth.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("question spawned work", err)
			}
			cmd := exec.Command(cli, "--version")
			cmd.Env = append(winFlowEnvironment(cfg), "OPENAI_API_KEY=synthetic-forbidden")
			if out, err := cmd.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("ambient credentials")) {
				t.Fatalf("credential boundary: %q %v", out, err)
			}
			// The same executable runs under the actual native Manager Job below.
			manager := session.New(filepath.Join(cfg.Root, "logs"))
			defer func() { winFlowCheck(t, manager.Shutdown(context.Background())) }()
			type result struct {
				out session.ProbeResult
				err error
			}
			for _, phase := range []string{"first", "replacement"} {
				controls.phase(t, root, phase)
				if phase == "replacement" {
					raw, err := os.ReadFile(filepath.Join(root, "out.txt"))
					winFlowCheck(t, err)
					if string(raw) != "approved output\n" {
						t.Fatal("normal release output", string(raw))
					}
					winFlowCheck(t, os.Remove(filepath.Join(root, "out.txt")))
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan result, 1)
				go func() {
					out, err := manager.Probe(ctx, []string{cli, "--model", "qa-native", "approved work"}, winFlowEnvironment(cfg), root)
					done <- result{out, err}
				}()
				// Every failure after launch cancels the original owned Job and joins Probe.
				joined := false
				t.Cleanup(func() {
					cancel()
					if !joined {
						<-done
					}
				})
				tree := controls.tree(t, root, phase)
				if tree.Core != nil {
					t.Fatal("component control falsely claimed a Core helper")
				}
				controls.observe(t, root, phase, false)
				controls.release(t, root, "first")
				if phase == "replacement" {
					controls.observe(t, root, phase, false)
					if signaled, err := winFlowEventSignaled(controls.roots[root].events[phase]); err != nil || signaled {
						t.Fatal("stale first event released replacement", err)
					}
					// Stop proof is through retained original handles, never PID rediscovery.
					cancel()
				}
				got := <-done
				joined = true
				cancel()
				if phase == "first" && (got.err != nil || string(got.out.Output) != "completed\n") {
					t.Fatalf("normal release: %+v", got)
				}
				if phase == "replacement" && got.err == nil {
					t.Fatal("canceled component succeeded")
				}
				if phase == "replacement" {
					winFlowCheck(t, manager.Shutdown(context.Background()))
				}
				controls.observe(t, root, phase, true)
				controls.foreignAlive(t)
			}
			if _, err := os.Stat(filepath.Join(root, "out.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("canceled replacement produced output", err)
			}
		})
	}
}

func TestWindowsPublicFlowJournalPrimitives(t *testing.T) {
	cfg := winFlowFixture(t, "codex", false)
	js, err := journal.Open(cfg.Root)
	winFlowCheck(t, err)
	const delivery = "component-lock"
	_, err = js.Append(delivery, journal.Record{Kind: loop.KindOpened, Detail: json.RawMessage(`{"slug":"component-only"}`)})
	winFlowCheck(t, err)
	lock, err := winFlowLockJournal(js.Path(delivery))
	winFlowCheck(t, err)
	defer func() { winFlowCheck(t, lock.close()) }()
	winFlowCheck(t, lock.proveBlocked())
	before, err := winFlowFileID(lock.file)
	winFlowCheck(t, err)
	winFlowCheck(t, lock.unlock())
	reader, err := os.Open(js.Path(delivery))
	winFlowCheck(t, err)
	defer reader.Close()
	snapshot, err := journal.ReadSnapshot(context.Background(), reader)
	if err != nil || !snapshot.Complete() || len(snapshot.Records()) != 1 {
		t.Fatal("unlocked snapshot", snapshot, err)
	}
	winFlowCheck(t, lock.lock())
	winFlowCheck(t, lock.corrupt())
	after, err := winFlowFileID(lock.file)
	winFlowCheck(t, err)
	if before != after {
		t.Fatal("fault replaced file identity")
	}
	winFlowCheck(t, lock.unlock())
	if _, err := journal.ReadSnapshot(context.Background(), reader); err == nil {
		t.Fatal("same-file invalid append was accepted")
	}
}

func TestWindowsPublicFlowParentBirthOrder(t *testing.T) {
	cfg := winFlowFixture(t, "codex", false)
	controls := newWinFlowControls(cfg)
	defer controls.close(t)
	self, err := winFlowBirthOf(windows.CurrentProcess(), uint32(os.Getpid()))
	winFlowCheck(t, err)
	later := controls.foreignStart(t).Root
	if later.Created <= self.Created {
		t.Fatal("foreign control was not born after this process", self, later)
	}
	valid, parent, err := winFlowParentBirth(later, self.PID)
	winFlowCheck(t, err)
	if parent != self {
		t.Fatal("real parent identity changed", parent, self)
	}
	winFlowCheck(t, windows.CloseHandle(valid))
	rejected, wrong, err := winFlowParentBirth(self, later.PID)
	if rejected != 0 {
		winFlowCheck(t, windows.CloseHandle(rejected))
	}
	if err == nil {
		t.Fatalf("later-born foreign process accepted as parent: child=%+v parent=%+v", self, wrong)
	}
	controls.foreignAlive(t)
}

func TestWindowsPublicFlowEventOwnership(t *testing.T) {
	cfg := winFlowFixture(t, "codex", false)
	c := newWinFlowControls(cfg)
	defer c.close(t)
	root := c.workspace(t, false, false)
	spec := c.roots[root].spec
	if _, err := winFlowCreateEvent(winFlowEventName(spec, "first")); err == nil {
		t.Fatal("existing event was adopted")
	}
	other := spec
	other.Fixture += "-different"
	if winFlowEventName(other, "first") == winFlowEventName(spec, "first") || winFlowEventName(spec, "first") == winFlowEventName(spec, "replacement") {
		t.Fatal("event identity collision")
	}
	foreign := c.foreignStart(t)
	wrong := foreign.Root
	wrong.Created++
	if handle, err := winFlowWitness(wrong); err == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("wrong creation-time witness accepted")
	}
	c.foreignAlive(t)
	c.release(t, root, "first")
	c.foreignAlive(t)
}

const winFlowCapabilityPrompt = "Run the shell command `git rev-parse HEAD` in the current directory. Do not create, edit or delete any file. When the command has run, print BATUTA-CAPABLE <sha> on a line of its own, replacing <sha> with the command's output, and stop."
const winFlowPlan = "# Plan — Public flow\n**Goal:** Use approved bytes\n**Status:** proposed\n\n## Tasks\n- [ ] 1. Create output — backend/low\n  Scope: out.txt\n  Accept: valid diff → git diff --check\n"
const winFlowPlanPath = ".batuta/plans/alpha.md"

type winFlowSpec struct {
	Common, Fixture, Phase string
	Ask, Core              bool
}
type winFlowBirth struct {
	PID     uint32
	Created uint64
}
type winFlowTree struct {
	Root, Child winFlowBirth
	Core        *winFlowBirth `json:",omitempty"`
}
type winFlowRoot struct {
	spec   winFlowSpec
	events map[string]windows.Handle
	trees  map[string][]windows.Handle
}
type winFlowForeign struct {
	log            *os.File
	cmd            *exec.Cmd
	event, witness windows.Handle
	birth          winFlowBirth
}
type winFlowControls struct {
	cfg     winFlowConfig
	roots   map[string]*winFlowRoot
	locks   map[string]*winFlowJournalLock
	foreign []*winFlowForeign
}

func newWinFlowControls(c winFlowConfig) *winFlowControls {
	return &winFlowControls{cfg: c, roots: map[string]*winFlowRoot{}, locks: map[string]*winFlowJournalLock{}}
}
func winFlowVersion(provider string) string {
	if provider == "claude" {
		return "2.1.289 (Claude Code)"
	}
	return "codex-cli 0.160.0"
}

// Provider and descendant modes exit before testing can print to their streams.
// Executable basename is fixed by the private fixture; no production flag exists.
func init() {
	name := strings.ToLower(filepath.Base(os.Args[0]))
	if len(os.Args) == 3 && os.Args[1] == "--windows-public-flow-descendant" {
		if err := winFlowWaitNamedEvent(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(92)
		}
		os.Exit(0)
	}
	if name == "codex.exe" || name == "claude.exe" {
		if err := winFlowProvider(strings.TrimSuffix(name, ".exe"), os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		os.Exit(0)
	}
}
func winFlowProvider(provider string, args []string) error {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_ACCESS_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		if os.Getenv(key) != "" {
			return errors.New("ambient credentials crossed provider boundary")
		}
	}
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			fmt.Println(winFlowVersion(provider))
			return nil
		case "debug":
			fmt.Println(`{"models":[{"slug":"qa-native"}]}`)
			return nil
		case "doctor", "plugin":
			fmt.Println("{}")
			return nil
		case "--verify":
			fmt.Println("<<<FINDINGS\nFINDINGS>>>\nTASK 1: DONE")
			return nil
		}
	}
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Stderr = os.Stderr
		raw, err := cmd.Output()
		return strings.TrimSpace(string(raw)), err
	}
	if len(args) == 3 && args[0] == "--model" && args[2] == winFlowCapabilityPrompt {
		sha, err := git("rev-parse", "HEAD")
		if err != nil {
			return err
		}
		fmt.Println("BATUTA-CAPABLE " + sha)
		return nil
	}
	common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	control := filepath.Join(common, "public-flow")
	raw, err := os.ReadFile(filepath.Join(control, "control.json"))
	if err != nil {
		return err
	}
	var spec winFlowSpec
	if err := winFlowDecode(raw, &spec); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(common)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(canonical), filepath.Clean(spec.Common)) || spec.Fixture == "" || (spec.Phase != "first" && spec.Phase != "replacement") {
		return errors.New("invalid canonical fixture binding")
	}
	if spec.Ask {
		f, err := os.OpenFile(filepath.Join(control, "asked"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			if err = f.Close(); err != nil {
				return err
			}
			fmt.Println("BATUTA-QUESTION: exact native question?")
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	event, err := winFlowOpenEvent(winFlowEventName(spec, spec.Phase))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	child := exec.Command(os.Args[0], "--windows-public-flow-descendant", winFlowEventName(spec, spec.Phase))
	// The child has no stdout handle, so it cannot corrupt provider or host protocol.
	child.Env, child.Stderr = os.Environ(), os.Stderr
	if err := child.Start(); err != nil {
		return err
	}
	joined := false
	defer func() {
		if !joined {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	root, err := winFlowBirthOf(windows.CurrentProcess(), uint32(os.Getpid()))
	if err != nil {
		return err
	}
	var born winFlowBirth
	var birthErr error
	if err := child.Process.WithHandle(func(h uintptr) { born, birthErr = winFlowBirthOf(windows.Handle(h), uint32(child.Process.Pid)) }); err != nil {
		return err
	}
	if birthErr != nil {
		return birthErr
	}
	tree := winFlowTree{Root: root, Child: born}
	if spec.Core {
		core, err := winFlowCoreAncestor()
		if err != nil {
			return err
		}
		tree.Core = &core
	}
	raw, err = json.Marshal(tree)
	if err != nil {
		return err
	}
	marker := filepath.Join(control, spec.Phase+".birth.json")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		return errors.New("phase birth already exists")
	}
	if err := os.WriteFile(marker+".tmp", raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(marker+".tmp", marker); err != nil {
		return err
	}
	status, err := windows.WaitForSingleObject(event, windows.INFINITE)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("provider release: %d %v", status, err)
	}
	err = child.Wait()
	joined = true
	if err != nil {
		return err
	}
	if err := os.WriteFile("out.txt", []byte("approved output\n"), 0600); err != nil {
		return err
	}
	fmt.Println("completed")
	return nil
}
func winFlowEventName(spec winFlowSpec, phase string) string {
	return fmt.Sprintf("Local\\BatutaPublicFlow-%x", sha256.Sum256([]byte(strings.ToLower(filepath.Clean(spec.Common))+"\x00"+spec.Fixture+"\x00"+phase)))
}
func winFlowCreateEvent(name string) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateEvent(nil, 1, 0, ptr)
	if err != nil {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return 0, err
	}
	return h, nil
}
func winFlowOpenEvent(name string) (windows.Handle, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.OpenEvent(windows.SYNCHRONIZE, false, ptr)
}
func winFlowWaitNamedEvent(name string) error {
	h, err := winFlowOpenEvent(name)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	status, err := windows.WaitForSingleObject(h, windows.INFINITE)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("descendant release: %d %v", status, err)
	}
	return nil
}
func winFlowEventSignaled(h windows.Handle) (bool, error) {
	status, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	if status != windows.WAIT_OBJECT_0 && status != uint32(windows.WAIT_TIMEOUT) {
		return false, fmt.Errorf("event wait: %d", status)
	}
	return status == windows.WAIT_OBJECT_0, nil
}
func winFlowBirthOf(h windows.Handle, pid uint32) (winFlowBirth, error) {
	var created, exited, kernel, user windows.Filetime
	err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user)
	return winFlowBirth{PID: pid, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, err
}
func winFlowWitness(birth winFlowBirth) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, birth.PID)
	if err != nil {
		return 0, err
	}
	got, err := winFlowBirthOf(h, birth.PID)
	if err != nil || got != birth || birth.Created == 0 {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("birth mismatch: %v %v %v", got, birth, err)
	}
	status, err := windows.WaitForSingleObject(h, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("birth already retired: %d %v", status, err)
	}
	return h, nil
}
func winFlowParentBirth(child winFlowBirth, parentPID uint32) (windows.Handle, winFlowBirth, error) {
	if child.PID == 0 || child.Created == 0 {
		return 0, winFlowBirth{}, errors.New("child birth is unknown")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, parentPID)
	if err != nil {
		return 0, winFlowBirth{}, fmt.Errorf("open parent %d for child %d: %w", parentPID, child.PID, err)
	}
	parent, err := winFlowBirthOf(h, parentPID)
	if err != nil {
		windows.CloseHandle(h)
		return 0, winFlowBirth{}, err
	}
	if parent.Created == 0 || parent.PID == child.PID || parent.Created > child.Created {
		windows.CloseHandle(h)
		return 0, winFlowBirth{}, errors.New("parent identity cannot precede this child")
	}
	return h, parent, nil
}

func winFlowCoreAncestor() (winFlowBirth, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return winFlowBirth{}, err
	}
	defer windows.CloseHandle(snapshot)
	entries := map[uint32]windows.ProcessEntry32{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		entries[entry.ProcessID] = entry
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return winFlowBirth{}, err
	}
	self, err := winFlowBirthOf(windows.CurrentProcess(), uint32(os.Getpid()))
	if err != nil {
		return winFlowBirth{}, err
	}
	witness, err := winFlowWitness(self)
	if err != nil {
		return winFlowBirth{}, err
	}
	defer windows.CloseHandle(witness)
	child := self
	for range 32 {
		entry, ok := entries[child.PID]
		if !ok {
			break
		}
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "consoled.exe") {
			return child, nil
		}
		parentHandle, parent, err := winFlowParentBirth(child, entry.ParentProcessID)
		if err != nil {
			return winFlowBirth{}, err
		}
		// Keep every observed object while validating the remaining ancestry.
		defer windows.CloseHandle(parentHandle)
		child = parent
	}
	return winFlowBirth{}, errors.New("actual Core helper ancestor absent")
}
func (c *winFlowControls) workspace(t *testing.T, ask, core bool) string {
	t.Helper()
	root, err := consoleruntime.NewPrivateTemp(c.cfg.Root, "workspace-")
	winFlowCheck(t, err)
	winFlowWrite(t, filepath.Join(root, winFlowPlanPath), []byte(winFlowPlan))
	winFlowWrite(t, filepath.Join(root, ".batuta", "profile.md"), []byte("# Profile\nTemplate: templates/generic.md\nStack: text\nMethodology: tests first\nTest: git diff --check\nExecution: sequential\nWorktree: always\n"))
	model := "qa-native"
	if c.cfg.Provider == "claude" {
		model = "sonnet"
	}
	routing := "# Routing\n| Lane | Domain | Executor | Model |\n|---|---|---|---|\n"
	for _, lane := range []string{"low", "medium", "high"} {
		routing += "| " + lane + " | * | " + c.cfg.Provider + " | " + model + " |\n"
	}
	routing += "| critical | * | self | session |\n\n| Role | Executor | Model |\n|---|---|---|\n| review | " + c.cfg.Provider + " | " + model + " |\n"
	winFlowWrite(t, filepath.Join(root, ".batuta", "routing.md"), []byte(routing))
	winFlowWrite(t, filepath.Join(root, ".gitignore"), []byte(".batuta/journal/\n.batuta/runs/\n.batuta/worktrees/\n"))
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "fixture"}, {"config", "user.email", "fixture@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "chore: native public fixture"}} {
		winFlowGit(t, c.cfg, root, args...)
	}
	common := winFlowGit(t, c.cfg, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	common, err = filepath.EvalSymlinks(common)
	winFlowCheck(t, err)
	spec := winFlowSpec{Common: common, Fixture: filepath.Base(root), Phase: "first", Ask: ask, Core: core}
	c.install(t, root, spec)
	winFlowWrite(t, filepath.Join(common, "public-flow", "control.json"), winFlowJSON(t, spec))
	return root
}
func (c *winFlowControls) install(t *testing.T, root string, spec winFlowSpec) {
	t.Helper()
	owned := &winFlowRoot{spec: spec, events: map[string]windows.Handle{}, trees: map[string][]windows.Handle{}}
	c.roots[root] = owned
	for _, phase := range []string{"first", "replacement"} {
		h, err := winFlowCreateEvent(winFlowEventName(spec, phase))
		winFlowCheck(t, err)
		owned.events[phase] = h
	}
}
func (c *winFlowControls) owned(t *testing.T, root string) *winFlowRoot {
	t.Helper()
	if own := c.roots[root]; own != nil {
		return own
	}
	relative, err := filepath.Rel(c.cfg.Root, root)
	winFlowCheck(t, err)
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		t.Fatal("control outside owned root")
	}
	common := winFlowGit(t, c.cfg, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	common, err = filepath.EvalSymlinks(common)
	winFlowCheck(t, err)
	var spec winFlowSpec
	raw, err := os.ReadFile(filepath.Join(common, "public-flow", "control.json"))
	winFlowCheck(t, err)
	winFlowCheck(t, winFlowDecode(raw, &spec))
	if !strings.EqualFold(common, spec.Common) || spec.Fixture != filepath.Base(root) {
		t.Fatal("workspace fixture binding mismatch")
	}
	c.install(t, root, spec)
	return c.roots[root]
}
func (c *winFlowControls) phase(t *testing.T, root, phase string) {
	t.Helper()
	if phase != "first" && phase != "replacement" {
		t.Fatal("invalid phase")
	}
	o := c.owned(t, root)
	o.spec.Phase = phase
	winFlowWrite(t, filepath.Join(o.spec.Common, "public-flow", "control.json"), winFlowJSON(t, o.spec))
}
func (c *winFlowControls) release(t *testing.T, root, phase string) {
	t.Helper()
	o := c.owned(t, root)
	h, ok := o.events[phase]
	if !ok {
		t.Fatal("unknown release phase")
	}
	winFlowCheck(t, windows.SetEvent(h))
}
func (c *winFlowControls) tree(t *testing.T, root, phase string) winFlowTree {
	t.Helper()
	o := c.owned(t, root)
	if phase != "first" && phase != "replacement" {
		t.Fatal("invalid birth phase")
	}
	path := filepath.Join(o.spec.Common, "public-flow", phase+".birth.json")
	var raw []byte
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var err error
		raw, err = os.ReadFile(path)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("native birth missing", ctx.Err())
		case <-tick.C:
		}
	}
	var tree winFlowTree
	winFlowCheck(t, winFlowDecode(raw, &tree))
	if o.spec.Core != (tree.Core != nil) {
		t.Fatal("Core/component witness mismatch")
	}
	if o.trees[phase] == nil {
		births := []winFlowBirth{tree.Root, tree.Child}
		if tree.Core != nil {
			births = append(births, *tree.Core)
		}
		for _, b := range births {
			h, err := winFlowWitness(b)
			winFlowCheck(t, err)
			o.trees[phase] = append(o.trees[phase], h)
		}
	}
	return tree
}
func (c *winFlowControls) observe(t *testing.T, root, phase string, retired bool) {
	t.Helper()
	o := c.owned(t, root)
	handles := o.trees[phase]
	if len(handles) < 2 {
		t.Fatal("original retained witnesses required")
	}
	for i, h := range handles {
		// Core/helper finalization may intentionally remain blocked by a held journal.
		if retired && i == 2 {
			continue
		}
		want := uint32(windows.WAIT_TIMEOUT)
		if retired {
			want = windows.WAIT_OBJECT_0
		}
		status, err := windows.WaitForSingleObject(h, 0)
		if err != nil || status != want {
			t.Fatalf("retained handle %d status=%d want=%d err=%v", i, status, want, err)
		}
	}
}
func (c *winFlowControls) foreignStart(t *testing.T) winFlowTree {
	t.Helper()
	spec := winFlowSpec{Common: c.cfg.Root, Fixture: fmt.Sprintf("foreign-%d", len(c.foreign))}
	event, err := winFlowCreateEvent(winFlowEventName(spec, "foreign"))
	winFlowCheck(t, err)
	handedOff := false
	defer func() {
		if !handedOff {
			_ = windows.CloseHandle(event)
		}
	}()
	log, err := os.OpenFile(filepath.Join(c.cfg.Root, fmt.Sprintf("foreign-%d.stderr", len(c.foreign))), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	winFlowCheck(t, err)
	defer func() {
		if !handedOff {
			_ = log.Close()
		}
	}()
	exe, err := os.Executable()
	winFlowCheck(t, err)
	cmd := exec.Command(exe, "--windows-public-flow-descendant", winFlowEventName(spec, "foreign"))
	cmd.Env = winFlowEnvironment(c.cfg)
	cmd.Stderr = &winFlowLog{file: log, remaining: winFlowLimit}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	handedOff = true
	f := &winFlowForeign{cmd: cmd, event: event, log: log}
	c.foreign = append(c.foreign, f)
	var birthErr error
	winFlowCheck(t, cmd.Process.WithHandle(func(h uintptr) { f.birth, birthErr = winFlowBirthOf(windows.Handle(h), uint32(cmd.Process.Pid)) }))
	winFlowCheck(t, birthErr)
	f.witness, err = winFlowWitness(f.birth)
	winFlowCheck(t, err)
	return winFlowTree{Root: f.birth}
}
func (c *winFlowControls) foreignAlive(t *testing.T) {
	t.Helper()
	for _, f := range c.foreign {
		status, err := windows.WaitForSingleObject(f.witness, 0)
		if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("foreign retired before owned cleanup", status, err)
		}
	}
}
func (c *winFlowControls) unlockAll(t *testing.T) {
	t.Helper()
	for key, lock := range c.locks {
		if err := lock.close(); err != nil {
			t.Error(err)
		}
		delete(c.locks, key)
	}
}
func (c *winFlowControls) close(t *testing.T) {
	t.Helper()
	c.unlockAll(t)
	for _, f := range c.foreign {
		if err := windows.SetEvent(f.event); err != nil {
			t.Error(err)
			_ = f.cmd.Process.Kill()
		}
		if err := errors.Join(f.cmd.Wait(), f.log.Close()); err != nil {
			t.Error(err)
		}
		if f.witness != 0 {
			if err := windows.CloseHandle(f.witness); err != nil {
				t.Error(err)
			}
		}
		if err := windows.CloseHandle(f.event); err != nil {
			t.Error(err)
		}
	}
	c.foreign = nil
	for key, o := range c.roots {
		for _, tree := range o.trees {
			for _, h := range tree {
				if err := windows.CloseHandle(h); err != nil {
					t.Error(err)
				}
			}
		}
		for _, event := range o.events {
			if err := windows.CloseHandle(event); err != nil {
				t.Error(err)
			}
		}
		delete(c.roots, key)
	}
}
func (c *winFlowControls) command(t *testing.T, cmd winFlowCommand) winFlowEvent {
	t.Helper()
	event := winFlowEvent{Kind: cmd.Op}
	switch cmd.Op {
	case "workspace":
		event.Root = c.workspace(t, cmd.Ask, true)
		event.Plan, event.PlanPath = winFlowPlan, winFlowPlanPath
	case "phase":
		c.phase(t, cmd.Root, cmd.Phase)
	case "release":
		c.release(t, cmd.Root, cmd.Phase)
	case "tree":
		tree := c.tree(t, cmd.Root, cmd.Phase)
		event.Tree = &tree
	case "alive", "retired":
		c.observe(t, cmd.Root, cmd.Phase, cmd.Op == "retired")
	case "core-alive", "core-retired":
		o := c.owned(t, cmd.Root)
		handles := o.trees[cmd.Phase]
		if len(handles) != 3 {
			t.Fatal("retained actual Core helper witness required")
		}
		want := uint32(windows.WAIT_TIMEOUT)
		if cmd.Op == "core-retired" {
			want = windows.WAIT_OBJECT_0
		}
		status, err := windows.WaitForSingleObject(handles[2], 0)
		if err != nil || status != want {
			t.Fatal("original Core helper observation", status, want, err)
		}
	case "foreign":
		tree := c.foreignStart(t)
		event.Tree = &tree
	case "foreign-alive":
		c.foreignAlive(t)
	case "hold":
		c.owned(t, cmd.Root)
		if cmd.Delivery == "" || filepath.Base(cmd.Delivery) != cmd.Delivery || strings.ContainsAny(cmd.Delivery, `/\:`) {
			t.Fatal("invalid delivery")
		}
		key := cmd.Root + "\x00" + cmd.Delivery
		if c.locks[key] != nil {
			t.Fatal("journal already held")
		}
		lock, err := winFlowLockJournal(filepath.Join(cmd.Root, ".batuta", "journal", cmd.Delivery+".jsonl"))
		winFlowCheck(t, err)
		c.locks[key] = lock
		winFlowCheck(t, lock.proveBlocked())
		event.Blocked = true
	case "unlock", "corrupt":
		key := cmd.Root + "\x00" + cmd.Delivery
		lock := c.locks[key]
		if lock == nil {
			t.Fatal("journal not held")
		}
		if cmd.Op == "corrupt" {
			c.observe(t, cmd.Root, cmd.Phase, true)
			winFlowCheck(t, lock.corrupt())
			event.SameFile = true
		}
		winFlowCheck(t, lock.close())
		delete(c.locks, key)
	default:
		event.Kind = "error"
		event.Error = "unknown private command"
	}
	return event
}

// Core55 locks one coordination byte outside the bounded journal data region.
// An independent file description must actually enter ReadSnapshot while held.
type winFlowJournalLock struct {
	file       *os.File
	overlapped windows.Overlapped
	held       bool
}
type winFlowIdentity struct{ Volume, High, Low uint32 }

func winFlowFileID(file *os.File) (winFlowIdentity, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
	return winFlowIdentity{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}, err
}
func winFlowLockJournal(path string) (*winFlowJournalLock, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	l := &winFlowJournalLock{file: file, overlapped: windows.Overlapped{Offset: (256 << 20) + 1}}
	if err := l.lock(); err != nil {
		file.Close()
		return nil, err
	}
	return l, nil
}
func (l *winFlowJournalLock) lock() error {
	if l.held {
		return errors.New("journal already locked")
	}
	err := windows.LockFileEx(windows.Handle(l.file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &l.overlapped)
	if err == nil {
		l.held = true
	}
	return err
}
func (l *winFlowJournalLock) unlock() error {
	if !l.held {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	if err == nil {
		l.held = false
	}
	return err
}
func (l *winFlowJournalLock) close() error {
	if l.file == nil {
		return nil
	}
	err := l.unlock()
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(err, closeErr)
}
func (l *winFlowJournalLock) proveBlocked() error {
	reader, err := os.Open(l.file.Name())
	if err != nil {
		return err
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	snapshot, err := journal.ReadSnapshot(ctx, reader)
	if !errors.Is(err, context.DeadlineExceeded) || snapshot.Complete() || len(snapshot.Records()) != 0 {
		return fmt.Errorf("coordination lock did not block actual ReadSnapshot: %+v %v", snapshot, err)
	}
	return nil
}
func (l *winFlowJournalLock) corrupt() error {
	if !l.held {
		return errors.New("fault requires held original journal")
	}
	before, err := winFlowFileID(l.file)
	if err != nil {
		return err
	}
	if _, err = l.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err = l.file.WriteString("not-a-journal-record\n"); err != nil {
		return err
	}
	if err = l.file.Sync(); err != nil {
		return err
	}
	after, err := winFlowFileID(l.file)
	if err != nil {
		return err
	}
	other, err := os.Open(l.file.Name())
	if err != nil {
		return err
	}
	defer other.Close()
	named, err := winFlowFileID(other)
	if err != nil {
		return err
	}
	if before != after || before != named {
		return errors.New("fault replaced original Windows file identity")
	}
	return nil
}
