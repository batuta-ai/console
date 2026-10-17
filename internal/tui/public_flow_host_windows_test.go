//go:build windows

package tui

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
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
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/api"
	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/store"
	"golang.org/x/sys/windows"
)

const winFlowLimit = 1 << 20

// Only the private bootstrap runs this list. testing output goes to diagnostics;
// stdout remains a typed protocol and helper PASS never enters the parent suite.
func TestMain(m *testing.M) {
	if os.Getenv("BATUTA_TEST_WINDOWS_PUBLIC_FLOW_CONFIG") != "" {
		events := os.Stdout
		os.Stdout = os.Stderr
		flag.Parse()
		if err := flag.Set("test.run", "^OwnedWindowsPublicFlowHost$"); err != nil {
			panic(err)
		}
		if err := flag.Set("test.skip", ""); err != nil {
			panic(err)
		}
		testing.Main(regexp.MatchString, []testing.InternalTest{{Name: "OwnedWindowsPublicFlowHost", F: func(t *testing.T) { winFlowServe(t, events) }}}, nil, nil)
		return
	}
	os.Exit(m.Run())
}

type winFlowConfig struct {
	Root, Home, Core, Helper, Provider, Bin, Skills, Git               string
	CoreSHA256, HelperSHA256, HostSHA256, SourceRevision, SourceDigest string
	GoVersion, CoreVersion                                             string
	SourceRoot, SourceMode                                             string
	Race                                                               bool
}
type winFlowCommand struct {
	Op                               string
	AccountID, Root, Phase, Delivery string
	Ask                              bool
}
type winFlowEvent struct {
	Kind                          string
	Error                         string         `json:",omitempty"`
	Address, Root, Plan, PlanPath string         `json:",omitempty"`
	PID                           int            `json:",omitempty"`
	Config                        *winFlowConfig `json:",omitempty"`
	Tree                          *winFlowTree   `json:",omitempty"`
	Blocked, SameFile, Joined     bool           `json:",omitempty"`
}

func winFlowCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func winFlowJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	winFlowCheck(t, err)
	return raw
}
func winFlowWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	winFlowCheck(t, consoleruntime.EnsurePrivateDirectory(filepath.Dir(path)))
	winFlowCheck(t, os.WriteFile(path, data, 0600))
}
func winFlowHash(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	winFlowCheck(t, err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func winFlowDecode(raw []byte, dst any) error {
	if len(raw) > winFlowLimit {
		return errors.New("private message exceeds bound")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("private message has trailing data")
	}
	return nil
}
func winFlowScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), winFlowLimit)
	return s
}

func winFlowFreshRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(os.TempDir(), "console-public-flow-"+rand.Text())
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	winFlowCheck(t, err)
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	winFlowCheck(t, err)
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	name, err := windows.UTF16PtrFromString(root)
	winFlowCheck(t, err)
	winFlowCheck(t, windows.CreateDirectory(name, &sa))
	root, err = filepath.EvalSymlinks(root)
	winFlowCheck(t, err)
	// Retained deliberately: failed joins must never lose their dependencies.
	t.Logf("retained owned Windows fixture: %s", root)
	return root
}
func winFlowEnvironment(c winFlowConfig) []string {
	home := filepath.Join(c.Root, "user")
	return []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "WINDIR=" + os.Getenv("SYSTEMROOT"), "SYSTEMDRIVE=" + os.Getenv("SYSTEMDRIVE"),
		"HOME=" + home, "USERPROFILE=" + home, "HOMEDRIVE=" + filepath.VolumeName(home), "HOMEPATH=" + strings.TrimPrefix(home, filepath.VolumeName(home)),
		"APPDATA=" + filepath.Join(home, "roaming"), "LOCALAPPDATA=" + filepath.Join(home, "local"),
		"TEMP=" + filepath.Join(c.Root, "tmp"), "TMP=" + filepath.Join(c.Root, "tmp"), "TMPDIR=" + filepath.Join(c.Root, "tmp"),
		"PATH=" + c.Bin + ";" + filepath.Dir(c.Git) + ";" + filepath.Join(filepath.Dir(filepath.Dir(c.Git)), "usr", "bin"),
		"BATUTA_SKILLS=" + filepath.Join(c.Skills, "batuta"), "BATUTA_LANG=en", "LANG=C", "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
}
func winFlowFixture(t *testing.T, provider string, build bool) winFlowConfig {
	t.Helper()
	if provider != "codex" && provider != "claude" {
		t.Fatal("invalid synthetic provider")
	}
	root := winFlowFreshRoot(t)
	git, err := exec.LookPath("git")
	winFlowCheck(t, err)
	c := winFlowConfig{Root: root, Home: filepath.Join(root, "home"), Core: filepath.Join(root, "batuta.exe"), Helper: filepath.Join(root, "consoled.exe"), Provider: provider, Bin: filepath.Join(root, "bin"), Skills: filepath.Join(root, "skills"), Git: git}
	for _, path := range []string{c.Home, c.Bin, filepath.Join(root, "tmp"), filepath.Join(root, "user", "roaming"), filepath.Join(root, "user", "local")} {
		winFlowCheck(t, consoleruntime.EnsurePrivateDirectory(path))
	}
	exe, err := os.Executable()
	winFlowCheck(t, err)
	c.HostSHA256 = winFlowHash(t, exe)
	raw, err := os.ReadFile(exe)
	winFlowCheck(t, err)
	winFlowWrite(t, filepath.Join(c.Bin, provider+".exe"), raw)
	for _, name := range []string{"batuta", "batuta-loop", "batuta-review"} {
		winFlowWrite(t, filepath.Join(c.Skills, name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: disposable public Windows fixture\n---\n"))
	}
	adapter := "---\nname: " + provider + "\nexecutable: " + provider + "\nrun: '" + provider + " --model {model} \"{brief}\"'\nreadonly: '" + provider + " --verify --model {model} \"{prompt}\"'\navailable: '" + provider + " --version'\nmodels: '" + provider + " debug models'\nmodel_flags: --model {model}\nfinished: exit_code\nbrief_limit_lines: 2000\ncwd_flag: env\n---\n"
	winFlowWrite(t, filepath.Join(c.Skills, "batuta", "adapters", provider+".md"), []byte(adapter))
	winFlowWrite(t, filepath.Join(c.Skills, "batuta", "templates", "generic.md"), []byte("# Generic\n## Conventions for briefs\n- Preserve approved scope.\nNever:\n- Edit beyond scope.\n"))
	if build {
		winFlowBuild(t, &c)
	}
	winFlowWrite(t, filepath.Join(root, "config.json"), winFlowJSON(t, c))
	return c
}
func winFlowBuild(t *testing.T, c *winFlowConfig) {
	t.Helper()
	if runtime.Version() != "go1.27.2" {
		t.Fatal("native Go1.27.2 required")
	}
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller source identity unavailable")
	}
	caller = filepath.FromSlash(caller)
	source := filepath.Dir(filepath.Dir(filepath.Dir(caller)))
	provenance, err := winFlowSource(source, caller, os.Getenv("BATUTA_TEST_WINDOWS_PUBLIC_FLOW_SOURCE_REVISION"), os.Getenv("BATUTA_TEST_WINDOWS_PUBLIC_FLOW_SOURCE_MANIFEST"), c.Git, winFlowEnvironment(*c))
	winFlowCheck(t, err)
	c.SourceRoot, c.SourceMode = source, provenance.Mode
	c.SourceRevision, c.SourceDigest = provenance.Revision, provenance.Digest
	winFlowWrite(t, filepath.Join(c.Root, "source.json"), winFlowJSON(t, provenance.Files))
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	hostExe, err := os.Executable()
	winFlowCheck(t, err)
	info, err := buildinfo.ReadFile(hostExe)
	winFlowCheck(t, err)
	c.Race = winFlowBinaryRace(info)
	winFlowCheck(t, winFlowBuildIdentity(info, "github.com/batuta-ai/console/internal/tui.test", c.Race))
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(goExe, args...)
		cmd.Dir = source
		// Build tools may need LLVM, but the executed fixture only gets the allowlist.
		cmd.Env = append(os.Environ(), "GOENV=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off", "GOROOT="+runtime.GOROOT(), "GOCACHE="+filepath.Join(c.Root, "go-cache"))
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native build %q: %s %v", args, raw, err)
		}
		return raw
	}
	winFlowWrite(t, filepath.Join(c.Root, "core-module.json"), run("list", "-m", "-json", "github.com/batuta-ai/core"))
	for _, entry := range []struct{ out, pkg string }{{c.Core, "github.com/batuta-ai/core/cmd/batuta"}, {c.Helper, "./cmd/consoled"}} {
		args := []string{"build", "-buildvcs=false"}
		if c.Race {
			args = append(args, "-race")
		}
		if entry.out == c.Core {
			args = append(args, "-ldflags", "-X main.buildVersion=v1.1.0-beta.55")
		}
		run(append(args, "-o", entry.out, entry.pkg)...)
	}
	c.CoreSHA256, c.HelperSHA256 = winFlowHash(t, c.Core), winFlowHash(t, c.Helper)
	winFlowVerifyBinary(t, c.Core, c.CoreSHA256, "github.com/batuta-ai/core/cmd/batuta", c.Race)
	winFlowVerifyBinary(t, c.Helper, c.HelperSHA256, "github.com/batuta-ai/console/cmd/consoled", c.Race)
	c.GoVersion = strings.TrimSpace(string(run("version")))
	cmd := exec.Command(c.Core, "--version")
	cmd.Env = winFlowEnvironment(*c)
	raw, err := cmd.CombinedOutput()
	winFlowCheck(t, err)
	c.CoreVersion = strings.TrimSpace(string(raw))
	if !strings.Contains(c.CoreVersion, "1.1.0-beta.55") {
		t.Fatal("actual compiled Core version", c.CoreVersion)
	}
	winFlowVerifySource(t, *c)
	winFlowWrite(t, filepath.Join(c.Root, "provenance.json"), winFlowJSON(t, c))
	t.Logf("native provenance %s", winFlowJSON(t, c))
}
func winFlowVerifyBinary(t *testing.T, path, digest, pkg string, race bool) {
	t.Helper()
	if digest == "" || winFlowHash(t, path) != digest {
		t.Fatal("binary SHA mismatch", path)
	}
	info, err := buildinfo.ReadFile(path)
	winFlowCheck(t, err)
	winFlowCheck(t, winFlowBuildIdentity(info, pkg, race))
}

func winFlowBinaryRace(info *debug.BuildInfo) bool {
	for _, setting := range info.Settings {
		if setting.Key == "-race" {
			return setting.Value == "true"
		}
	}
	return false
}

func winFlowBuildIdentity(info *debug.BuildInfo, pkg string, race bool) error {
	if info.GoVersion != "go1.27.2" || info.Path != pkg {
		return errors.New("binary toolchain/package identity mismatch")
	}
	main := "github.com/batuta-ai/console"
	if pkg == "github.com/batuta-ai/core/cmd/batuta" {
		main = "github.com/batuta-ai/core"
	}
	if info.Main.Path != main {
		return errors.New("binary main module mismatch")
	}
	pinned := false
	for _, m := range append(info.Deps, &info.Main) {
		if m.Replace != nil {
			return fmt.Errorf("binary replaced module: %s", m.Path)
		}
		if m.Path == "github.com/batuta-ai/core" {
			if pinned || m.Version != "v1.1.0-beta.55" || m.Sum != "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y=" {
				return errors.New("binary Core55 module/h1 mismatch")
			}
			pinned = true
		}
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		if _, exists := settings[s.Key]; exists {
			return errors.New("duplicate binary setting")
		}
		settings[s.Key] = s.Value
	}
	if !pinned || settings["GOOS"] != "windows" || settings["GOARCH"] != "amd64" || (settings["-race"] == "true") != race || (settings["CGO_ENABLED"] != "0" && settings["CGO_ENABLED"] != "1") || (race && settings["CGO_ENABLED"] != "1") {
		return errors.New("binary Core55/native amd64/CGO/race mismatch")
	}
	return nil
}

type winFlowSourceProof struct {
	Revision, Digest, Mode string
	Files                  map[string]string
}

func winFlowCanonical(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("noncanonical absolute path: %q", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return fmt.Errorf("aliased source path: %q -> %q", path, resolved)
	}
	return nil
}

func winFlowSourceName(name string) error {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || !filepath.IsLocal(filepath.FromSlash(name)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name {
		return fmt.Errorf("noncanonical manifest name: %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if strings.TrimRight(part, ". ") != part || strings.EqualFold(part, ".git") {
			return fmt.Errorf("reserved manifest name: %q", name)
		}
	}
	return nil
}

func winFlowManifest(path string) (map[string]string, error) {
	if err := winFlowCanonical(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, winFlowLimit+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if len(raw) > winFlowLimit {
		return nil, errors.New("source manifest exceeds bound")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("source manifest must be an object")
	}
	files := map[string]string{}
	folded := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("manifest key is not a path")
		}
		if err := winFlowSourceName(name); err != nil {
			return nil, err
		}
		if folded[strings.ToLower(name)] {
			return nil, fmt.Errorf("duplicate/aliased manifest path: %s", name)
		}
		folded[strings.ToLower(name)] = true
		var digest string
		if err := d.Decode(&digest); err != nil {
			return nil, err
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
			return nil, fmt.Errorf("invalid SHA256 for %s", name)
		}
		files[name] = digest
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing source manifest data")
	}
	return files, nil
}

func winFlowSource(source, caller, revision, manifestPath, git string, env []string) (winFlowSourceProof, error) {
	proof := winFlowSourceProof{Revision: revision, Mode: "archive"}
	if err := winFlowCanonical(source); err != nil {
		return proof, err
	}
	if err := winFlowCanonical(caller); err != nil {
		return proof, err
	}
	if caller != filepath.Join(source, "internal", "tui", "public_flow_host_windows_test.go") {
		return proof, errors.New("runtime.Caller is not the manifested host source")
	}
	var err error
	if revision != "" || manifestPath != "" {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) || manifestPath == "" {
			return proof, errors.New("archive requires exact 40hex revision and external manifest")
		}
		var rel string
		rel, err = filepath.Rel(source, manifestPath)
		if err != nil || filepath.IsLocal(rel) || rel == "." {
			return proof, errors.New("archive manifest must be outside source")
		}
		if _, err := os.Lstat(filepath.Join(source, ".git")); !errors.Is(err, os.ErrNotExist) {
			return proof, errors.New("archive source must not contain Git metadata")
		}
		proof.Files, err = winFlowManifest(manifestPath)
	} else {
		proof.Mode = "git"
		proof.Revision, proof.Files, err = winFlowGitSource(source, git, env)
	}
	if err != nil {
		return proof, err
	}
	proof.Digest, err = winFlowSourceFiles(source, proof.Files, proof.Mode == "git")
	return proof, err
}

func winFlowGitSource(source, git string, env []string) (string, map[string]string, error) {
	if git == "" {
		return "", nil, errors.New("no explicit archive metadata or Git executable")
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(git, args...)
		cmd.Dir, cmd.Env = source, env
		raw, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("source Git %q: %s: %w", args, raw, err)
		}
		return raw, nil
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, err
	}
	if filepath.FromSlash(strings.TrimSpace(string(top))) != source {
		return "", nil, errors.New("source is not Git top level")
	}
	head, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", nil, err
	}
	revision := strings.TrimSpace(string(head))
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
		return "", nil, errors.New("Git revision is not exact 40hex")
	}
	status, err := run("status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", nil, err
	}
	if len(status) != 0 {
		return "", nil, errors.New("source Git checkout is not clean")
	}
	tree, err := run("ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if err != nil {
		return "", nil, err
	}
	files := map[string]string{}
	for _, entry := range strings.Split(string(tree), "\x00") {
		if entry == "" {
			continue
		}
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
			return "", nil, errors.New("nonregular Git source entry")
		}
		if err := winFlowSourceName(name); err != nil {
			return "", nil, err
		}
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := winFlowCanonical(path); err != nil {
			return "", nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		// Compare to HEAD itself; index flags and clean filters are not byte proof.
		blob := sha1.New()
		fmt.Fprintf(blob, "blob %d\x00", len(raw))
		_, _ = blob.Write(raw)
		if hex.EncodeToString(blob.Sum(nil)) != fields[2] {
			return "", nil, fmt.Errorf("source bytes differ from Git HEAD: %s", name)
		}
		files[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	return revision, files, nil
}

func winFlowSourceFiles(source string, files map[string]string, git bool) (string, error) {
	for _, required := range []string{"go.mod", "go.sum", "internal/tui/public_flow_host_windows_test.go", "internal/tui/public_flow_native_windows_test.go"} {
		if files[required] == "" {
			return "", fmt.Errorf("missing required source: %s", required)
		}
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if git && name == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := winFlowSourceName(name); err != nil {
			return err
		}
		if err := winFlowCanonical(path); err != nil {
			return err
		}
		// These generated roots are outside the fixed Go build inputs. A tracked
		// file there still fails the complete manifest check below.
		if git && entry.IsDir() && (name == ".batuta/runs" || name == "web/node_modules") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("nonregular source: %s", name)
		}
		want, ok := files[name]
		if !ok {
			return fmt.Errorf("unmanifested source: %s", name)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
			return fmt.Errorf("source SHA256 mismatch: %s", name)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(seen) != len(files) {
		return "", errors.New("manifest includes missing source files")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		fmt.Fprintf(digest, "%s\x00%s\n", name, files[name])
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func winFlowVerifySource(t *testing.T, cfg winFlowConfig) {
	t.Helper()
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller source unavailable")
	}
	revision, manifest := "", ""
	switch cfg.SourceMode {
	case "archive":
		revision, manifest = cfg.SourceRevision, filepath.Join(cfg.Root, "source.json")
	case "git":
	default:
		t.Fatal("unknown source provenance mode")
	}
	proof, err := winFlowSource(cfg.SourceRoot, filepath.FromSlash(caller), revision, manifest, cfg.Git, winFlowEnvironment(cfg))
	winFlowCheck(t, err)
	if proof.Revision != cfg.SourceRevision || proof.Digest != cfg.SourceDigest || proof.Mode != cfg.SourceMode {
		t.Fatal("source changed since qualification build")
	}
}

func winFlowGit(t *testing.T, c winFlowConfig, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(c.Git, args...)
	cmd.Dir, cmd.Env = dir, winFlowEnvironment(c)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func winFlowServe(t *testing.T, events *os.File) {
	raw, err := os.ReadFile(os.Getenv("BATUTA_TEST_WINDOWS_PUBLIC_FLOW_CONFIG"))
	winFlowCheck(t, err)
	var cfg winFlowConfig
	winFlowCheck(t, winFlowDecode(raw, &cfg))
	winFlowVerifyBinary(t, cfg.Core, cfg.CoreSHA256, "github.com/batuta-ai/core/cmd/batuta", cfg.Race)
	winFlowVerifyBinary(t, cfg.Helper, cfg.HelperSHA256, "github.com/batuta-ai/console/cmd/consoled", cfg.Race)
	winFlowVerifyBinary(t, os.Args[0], cfg.HostSHA256, "github.com/batuta-ai/console/internal/tui.test", cfg.Race)
	winFlowVerifySource(t, cfg)
	paths, err := consoleruntime.Resolve(cfg.Home, "127.0.0.1:0")
	winFlowCheck(t, err)
	token, err := consoleruntime.LoadOrCreateAuth(paths.Auth)
	winFlowCheck(t, err)
	secret, err := consoleruntime.OpenPrivateFile(paths.Auth)
	winFlowCheck(t, err)
	winFlowCheck(t, secret.Close())
	st, err := store.Open(paths.DB)
	winFlowCheck(t, err)
	manager := session.New(paths.Logs)
	svc := service.New(st, manager, nil)
	svc.Home, svc.CoreExecutable, svc.NativeExecutorExecutable = cfg.Home, cfg.Core, cfg.Helper
	controls := newWinFlowControls(cfg)
	var mu sync.Mutex
	closing := false
	var handlers sync.WaitGroup
	base, cancelHTTP := context.WithCancel(context.Background())
	handler := (&api.Server{Svc: svc, Token: token}).Handler()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return base }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closing {
			mu.Unlock()
			http.Error(w, "fixture closing", 503)
			return
		}
		handlers.Add(1)
		mu.Unlock()
		defer handlers.Done()
		handler.ServeHTTP(w, r)
	})}
	var served chan error
	enc := json.NewEncoder(events)
	defer func() {
		mu.Lock()
		closing = true
		mu.Unlock()
		cancelHTTP()
		closeErr := server.Close()
		if served != nil {
			if e := <-served; !errors.Is(e, http.ErrServerClosed) {
				closeErr = errors.Join(closeErr, e)
			}
		}
		// Unlock first so accepted Stop finalizers can finish. No successful Store close
		// is possible until all admitted handlers and native ownership have joined.
		controls.unlockAll(t)
		nativeErr := svc.ShutdownNative(context.Background())
		managerErr := manager.Shutdown(context.Background())
		handlers.Wait()
		controls.close(t)
		if t.Failed() {
			t.Log("retaining Store after failed fixture cleanup")
			return
		}
		if err := errors.Join(closeErr, nativeErr, managerErr); err != nil {
			t.Errorf("drain uncertain; retaining Store and all dependencies: %v", err)
			return
		}
		winFlowCheck(t, st.Close())
		winFlowCheck(t, enc.Encode(winFlowEvent{Kind: "drained", Joined: true}))
	}()
	winFlowCheck(t, manager.HostError())
	winFlowCheck(t, svc.ReconcileSessions())
	accounts, err := st.ListAccounts()
	winFlowCheck(t, err)
	for _, account := range accounts {
		journeyFixtureAuthentication(t, svc, account)
	}
	ln, err := net.Listen("tcp", paths.Listen)
	winFlowCheck(t, err)
	served = make(chan error, 1)
	go func() { served <- server.Serve(ln) }()
	address := ln.Addr().String()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	for _, auth := range []bool{false, true} {
		req, err := http.NewRequest("GET", "http://"+address+"/v0/capabilities", nil)
		winFlowCheck(t, err)
		want := 401
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
			want = 200
		}
		res, err := client.Do(req)
		winFlowCheck(t, err)
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, winFlowLimit))
		winFlowCheck(t, errors.Join(err, res.Body.Close()))
		if res.StatusCode != want {
			t.Fatal("public capabilities authentication", res.StatusCode, want)
		}
	}
	winFlowCheck(t, enc.Encode(winFlowEvent{Kind: "ready", Address: address, PID: os.Getpid(), Config: &cfg}))
	scan := winFlowScanner(os.Stdin)
	for scan.Scan() {
		var command winFlowCommand
		if err := winFlowDecode(scan.Bytes(), &command); err != nil {
			winFlowCheck(t, enc.Encode(winFlowEvent{Kind: "error", Error: err.Error()}))
			continue
		}
		event := winFlowEvent{Kind: command.Op}
		switch command.Op {
		case "drain":
			return
		case "authenticate":
			account, err := st.GetAccount(command.AccountID)
			winFlowCheck(t, err)
			if account.Adapter != cfg.Provider {
				t.Fatal("account provider mismatch")
			}
			account.SessionCap = 2
			winFlowCheck(t, st.PutAccount(account))
			journeyFixtureAuthentication(t, svc, account)
			event.Kind = "authenticated"
		default:
			event = controls.command(t, command)
		}
		winFlowCheck(t, enc.Encode(event))
	}
	winFlowCheck(t, scan.Err())
}

type winFlowLog struct {
	mu        sync.Mutex
	file      *os.File
	remaining int
}

func (l *winFlowLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if len(p) > l.remaining {
		p = p[:l.remaining]
	}
	if len(p) > 0 {
		w, err := l.file.Write(p)
		l.remaining -= w
		if err != nil {
			return w, err
		}
	}
	return n, nil
}

type winFlowHost struct {
	cfg            winFlowConfig
	cmd            *exec.Cmd
	input, output  *os.File
	enc            *json.Encoder
	events         chan winFlowEvent
	readDone       chan struct{}
	readErr        error
	done           chan error
	address, token string
	closed         bool
	joined         bool
	drained        bool
}

func winFlowLaunch(t *testing.T, cfg winFlowConfig) *winFlowHost {
	t.Helper()
	path := filepath.Join(cfg.Root, "config.json")
	winFlowWrite(t, path, winFlowJSON(t, cfg))
	childIn, input, err := os.Pipe()
	winFlowCheck(t, err)
	handedOff := false
	defer func() {
		_ = childIn.Close()
		if !handedOff {
			_ = input.Close()
		}
	}()
	output, childOut, err := os.Pipe()
	if err != nil {
		childIn.Close()
		input.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = childOut.Close()
		if !handedOff {
			_ = output.Close()
		}
	}()
	exe, err := os.Executable()
	winFlowCheck(t, err)
	cmd := exec.Command(exe, "-test.timeout=3m")
	cmd.Env = append(winFlowEnvironment(cfg), "BATUTA_TEST_WINDOWS_PUBLIC_FLOW_CONFIG="+path)
	cmd.Stdin, cmd.Stdout = childIn, childOut
	log, err := os.OpenFile(filepath.Join(cfg.Root, "host-"+rand.Text()+".stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	winFlowCheck(t, err)
	cmd.Stderr = &winFlowLog{file: log, remaining: winFlowLimit}
	err = cmd.Start()
	childIn.Close()
	childOut.Close()
	if err != nil {
		input.Close()
		output.Close()
		log.Close()
		t.Fatal(err)
	}
	handedOff = true
	h := &winFlowHost{cfg: cfg, cmd: cmd, input: input, output: output, enc: json.NewEncoder(input), events: make(chan winFlowEvent, 16), readDone: make(chan struct{}), done: make(chan error, 1)}
	go func() { h.done <- errors.Join(cmd.Wait(), log.Close()) }()
	go func() {
		defer close(h.readDone)
		defer close(h.events)
		scan := winFlowScanner(output)
		for scan.Scan() {
			var e winFlowEvent
			if err := winFlowDecode(scan.Bytes(), &e); err != nil {
				h.readErr = err
				return
			}
			select {
			case h.events <- e:
			default:
				h.readErr = errors.New("unconsumed private events exceed bound")
				return
			}
		}
		h.readErr = scan.Err()
	}()
	t.Cleanup(func() { h.close(t) })
	event := h.next(t)
	if event.Kind != "ready" || event.PID != cmd.Process.Pid || event.Config == nil || *event.Config != cfg {
		t.Fatal("host ready identity", event)
	}
	h.address = event.Address
	token, err := os.ReadFile(filepath.Join(cfg.Home, "auth.secret"))
	winFlowCheck(t, err)
	h.token = strings.TrimSpace(string(token))
	return h
}
func (h *winFlowHost) next(t *testing.T) winFlowEvent {
	t.Helper()
	select {
	case event, ok := <-h.events:
		if !ok {
			<-h.readDone
			t.Fatal("private stream ended", h.readErr)
		}
		if event.Kind == "drained" && event.Joined {
			h.drained = true
		}
		return event
	case <-time.After(30 * time.Second):
		t.Fatal("private protocol response missing; fixture retained")
	}
	return winFlowEvent{}
}
func (h *winFlowHost) control(t *testing.T, c winFlowCommand) winFlowEvent {
	t.Helper()
	raw := winFlowJSON(t, c)
	if len(raw) > winFlowLimit {
		t.Fatal("oversized command")
	}
	winFlowCheck(t, h.enc.Encode(c))
	return h.next(t)
}
func (h *winFlowHost) close(t *testing.T) {
	t.Helper()
	if h.closed {
		return
	}
	h.closed = true
	_ = h.input.Close()
	select {
	case err := <-h.done:
		<-h.readDone
		_ = h.output.Close()
		drained := h.drained
		for event := range h.events {
			if event.Kind == "drained" && event.Joined {
				drained = true
			}
		}
		h.joined = err == nil && h.readErr == nil && drained
		if !h.joined {
			t.Errorf("host EOF/full join: drained=%v process=%v reader=%v; retained %s", drained, err, h.readErr, h.cfg.Root)
		}
	case <-time.After(30 * time.Second):
		_ = h.output.Close()
		<-h.readDone
		t.Errorf("host join uncertain; no force kill or dependency deletion: pid=%d root=%s", h.cmd.Process.Pid, h.cfg.Root)
	}
}

func TestWindowsPublicFlowHostEOF(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := winFlowFixture(t, provider, true)
			h := winFlowLaunch(t, cfg)
			if e := h.control(t, winFlowCommand{Op: "unknown"}); e.Kind != "error" {
				t.Fatal("unknown command accepted", e)
			}
			_, err := h.input.WriteString("{\"Op\":\"drain\",\"extra\":true}\n")
			winFlowCheck(t, err)
			if e := h.next(t); e.Kind != "error" {
				t.Fatal("malformed command affected admission", e)
			}
			a := h.control(t, winFlowCommand{Op: "workspace"})
			b := h.control(t, winFlowCommand{Op: "workspace", Ask: true})
			if a.Root == "" || a.Root == b.Root || a.PlanPath != winFlowPlanPath {
				t.Fatal("independent canonical fixtures", a, b)
			}
			foreign := h.control(t, winFlowCommand{Op: "foreign"})
			if foreign.Tree == nil {
				t.Fatal(foreign)
			}
			// One actual SDK run qualifies EOF ownership. Dual-root Stop and
			// restart-to-SHIP journeys belong to the following slice.
			tree := winFlowStartForDrain(t, h, a.Root)
			births := []winFlowBirth{tree.Root, tree.Child, *tree.Core, foreign.Tree.Root}
			var witnesses []windows.Handle
			for _, birth := range births {
				w, err := winFlowWitness(birth)
				winFlowCheck(t, err)
				witnesses = append(witnesses, w)
				t.Cleanup(func() { winFlowCheck(t, windows.CloseHandle(w)) })
			}
			if e := h.control(t, winFlowCommand{Op: "foreign-alive"}); e.Kind != "foreign-alive" {
				t.Fatal(e)
			}
			h.close(t)
			if !h.joined {
				t.Fatal("old host drain unresolved; refusing restart")
			}
			for _, w := range witnesses {
				status, err := windows.WaitForSingleObject(w, 0)
				if err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("EOF left original process alive", status, err)
				}
			}
			// The same Store must reopen after the old host's EOF drain.
			again := winFlowLaunch(t, cfg)
			again.close(t)
		})
	}
}
func TestWindowsPublicFlowProtocolBounds(t *testing.T) {
	for _, raw := range []string{`{"Op":"drain","extra":true}`, `{"Op":"drain"} {}`, strings.Repeat("x", winFlowLimit+1)} {
		var cmd winFlowCommand
		if winFlowDecode([]byte(raw), &cmd) == nil {
			t.Fatal("invalid bounded protocol accepted")
		}
	}
	scan := winFlowScanner(strings.NewReader(strings.Repeat("x", winFlowLimit+1) + "\n"))
	if scan.Scan() || scan.Err() == nil {
		t.Fatal("oversized line accepted")
	}
}

func winFlowStartForDrain(t *testing.T, h *winFlowHost, root string) winFlowTree {
	t.Helper()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	apiClient := client.New("http://"+h.address, h.token)
	apiClient.HTTP = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	account, err := apiClient.CreateAccount(ctx, client.CreateAccountRequest{Label: "owned", Adapter: h.cfg.Provider})
	winFlowCheck(t, err)
	if e := h.control(t, winFlowCommand{Op: "authenticate", AccountID: account.ID}); e.Kind != "authenticated" {
		t.Fatal(e)
	}
	account, err = apiClient.ConnectAccount(ctx, account.ID)
	winFlowCheck(t, err)
	if !account.Connected || account.SessionCap != 2 {
		t.Fatal("synthetic account readiness/capacity", account)
	}
	workspace, err := apiClient.CreateWorkspace(ctx, client.CreateWorkspaceRequest{Name: "owned", Root: root})
	winFlowCheck(t, err)
	model := "qa-native"
	if h.cfg.Provider == "claude" {
		model = "sonnet"
	}
	squad, err := apiClient.SaveSquad(ctx, domain.Squad{Name: "owned", Members: []domain.Member{{Role: "lead", CLI: h.cfg.Provider, Model: model, AccountID: account.ID, Skills: []string{"batuta-loop", "batuta-review"}, Maestro: true}}})
	winFlowCheck(t, err)
	mission, err := apiClient.CreateMission(ctx, client.CreateMissionRequest{WorkspaceID: workspace.ID, Title: "EOF lifetime", SquadID: squad.ID})
	winFlowCheck(t, err)
	mission, err = apiClient.BindMission(ctx, mission.ID, client.MissionBindingRequest{WorkspaceID: workspace.ID, PlanPath: winFlowPlanPath})
	winFlowCheck(t, err)
	detail, err := apiClient.PlanDetail(ctx, mission.ID, workspace.ID)
	winFlowCheck(t, err)
	if !bytes.Equal(detail.Content, []byte(winFlowPlan)) || detail.Identity.Version != domain.PlanVersion(detail.Content) {
		t.Fatal("exact plan bytes", detail)
	}
	approved, err := apiClient.ApprovePlan(ctx, detail.Identity)
	winFlowCheck(t, err)
	if approved.Receipt == nil || approved.Effect != domain.PlanApprovalRecorded || approved.Receipt.Identity != detail.Identity || !bytes.Equal(approved.Receipt.Content, detail.Content) {
		t.Fatal("exact approval", approved)
	}
	run, err := apiClient.StartExecution(ctx, domain.ExecutionRequest{Target: detail.Identity, ApprovalID: approved.Receipt.ID})
	winFlowCheck(t, err)
	if run.Effect != domain.ExecutionStarted || !domain.ValidExecutionRunID(run.RunID) || run.OpenedDigest == "" {
		t.Fatal("actual SDK admission", run)
	}
	// The accepted run outlives the completed HTTP connection and caller context.
	cancel()
	transport.CloseIdleConnections()
	event := h.control(t, winFlowCommand{Op: "tree", Root: root, Phase: "first"})
	if event.Tree == nil || event.Tree.Core == nil {
		t.Fatal("actual helper/provider/descendant witnesses", event)
	}
	if e := h.control(t, winFlowCommand{Op: "alive", Root: root, Phase: "first"}); e.Kind != "alive" {
		t.Fatal(e)
	}
	t.Logf("actual Core SDK run admitted via public client/API: delivery=%s run=%s tree=%+v; no SHIP claim", run.Delivery, run.RunID, *event.Tree)
	return *event.Tree
}
