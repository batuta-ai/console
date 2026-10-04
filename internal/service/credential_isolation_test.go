package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
)

var credentialSentinels = []string{
	"OPENAI_API_KEY", "CODEX_ACCESS_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
	"GOOGLE_APPLICATION_CREDENTIALS", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "NODE_OPTIONS", "BASH_ENV",
	"ENV", "UNREVIEWED_CREDENTIAL", "BATUTA_ROLE_SKILLS",
}

var identityKeys = []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CURSOR_CONFIG_DIR"}

var batutaMetadataKeys = []string{"BATUTA_CONSOLE", "BATUTA_WORKSPACE", "BATUTA_SKILLS", "BATUTA_JOURNAL", "BATUTA_ROLE_SKILLS", "BATUTA_MISSION", "BATUTA_INIT"}

func fixtureRuntime(root, cli string) map[string]string {
	return map[string]string{
		"PATH": cli, "LANG": "C", "LC_ALL": "C", "LC_CTYPE": "C", "LC_COLLATE": "C", "LC_MESSAGES": "C", "LC_MONETARY": "C", "LC_NUMERIC": "C", "LC_TIME": "C", "LC_ADDRESS": "C", "LC_IDENTIFICATION": "C", "LC_MEASUREMENT": "C", "LC_NAME": "C", "LC_PAPER": "C", "LC_TELEPHONE": "C",
		"TERM": "synthetic-daemon-terminal", "COLORTERM": "truecolor", "SHELL": "/bin/sh", "DISPLAY": "synthetic-display", "WAYLAND_DISPLAY": "synthetic-wayland", "XDG_RUNTIME_DIR": filepath.Join(root, "runtime"), "DBUS_SESSION_BUS_ADDRESS": "unix:path=" + filepath.Join(root, "synthetic-dbus"),
		"SSL_CERT_FILE": filepath.Join(root, "synthetic-ca.pem"), "SSL_CERT_DIR": filepath.Join(root, "synthetic-certs"), "NODE_EXTRA_CA_CERTS": filepath.Join(root, "synthetic-node-ca.pem"), "CODEX_CA_CERTIFICATE": filepath.Join(root, "synthetic-codex-ca.pem"),
	}
}

type credentialRecord struct {
	NativeSkills map[string]string `json:"native_skills"`
	Env          map[string]string `json:"env"`
	Identity     string            `json:"identity"`
	CWD          string            `json:"cwd"`
	Argv         []string          `json:"argv"`
	PTY          bool              `json:"pty"`
}

// The real disposable subprocess reads only named keys seeded by its parent.
// It never reads a credential file outside this fixture's synthetic tree.
func TestCredentialRecordChild(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "console-credential-fixture" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	out, fixtureRoot, adapter := os.Args[at+1], os.Args[at+2], os.Args[at+3]
	record := credentialRecord{Env: map[string]string{}}
	record.Argv = os.Args[at+4:]
	input, inputErr := os.Stdin.Stat()
	output, outputErr := os.Stdout.Stat()
	record.PTY = inputErr == nil && outputErr == nil && input.Mode()&os.ModeCharDevice != 0 && output.Mode()&os.ModeCharDevice != 0
	keys := append(append([]string{}, credentialSentinels...), identityKeys...)
	for key := range fixtureRuntime("", "") {
		keys = append(keys, key)
	}
	keys = append(keys, batutaMetadataKeys...)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			record.Env[key] = value
		}
	}
	record.CWD, _ = os.Getwd()
	// Discovery uses only native paths, independently of Batuta's core hint.
	if err := os.Unsetenv("BATUTA_SKILLS"); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(record.Env["HOME"], ".agents", "skills")
	if adapter == "claude" {
		native = filepath.Join(record.Env["CLAUDE_CONFIG_DIR"], "skills")
	}
	record.NativeSkills = map[string]string{}
	entries, _ := os.ReadDir(native)
	for _, entry := range entries {
		if info, err := os.Stat(filepath.Join(native, entry.Name(), "SKILL.md")); err == nil && info.Mode().IsRegular() {
			target, err := filepath.EvalSymlinks(filepath.Join(native, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			record.NativeSkills[entry.Name()] = target
		}
	}
	config, file := record.Env["CODEX_HOME"], "auth.json"
	if adapter == "claude" {
		config, file = record.Env["CLAUDE_CONFIG_DIR"], ".credentials.json"
	}
	target := filepath.Join(config, file)
	relative, err := filepath.Rel(fixtureRoot, target)
	if config != "" && err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		if data, err := os.ReadFile(target); err == nil {
			var auth struct {
				Identity string `json:"synthetic_identity"`
			}
			if json.Unmarshal(data, &auth) == nil {
				record.Identity = auth.Identity
			}
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0600); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if line == "exit\n" {
			return
		}
		fmt.Printf("ack:%s", line)
	}
}

func isolationFixture(t *testing.T, adapter string) (*Service, domain.Workspace, domain.Mission, domain.Account, string, string) {
	t.Helper()
	root := t.TempDir()
	svc := testSvc(t)
	svc.Home = filepath.Join(root, "console")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Session.Shutdown(ctx); err != nil {
			t.Errorf("fixture drain: %v", err)
		}
	})
	out := filepath.Join(root, "record.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "bin")
	if err := os.Mkdir(cli, 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	banner := "codex-cli 0.160.0"
	if adapter == "claude" {
		banner = "2.1.289 (Claude Code)"
	}
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + banner + "'; exit 0; fi\nexec " + quote(executable) + " -test.run '^TestCredentialRecordChild$' -- console-credential-fixture " + quote(out) + " " + quote(root) + " " + quote(adapter) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(cli, adapter), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for key, value := range fixtureRuntime(root, cli) {
		t.Setenv(key, value)
	}
	for _, key := range batutaMetadataKeys {
		t.Setenv(key, "synthetic-daemon-"+key)
	}
	t.Setenv("BATUTA_SKILLS", filepath.Join(root, "skills"))
	installedSkillFixture(t, filepath.Join(root, "skills"), "batuta", "batuta-init", "batuta-plan")
	for _, key := range credentialSentinels {
		t.Setenv(key, "synthetic-daemon-"+key)
	}
	for _, key := range identityKeys {
		path := filepath.Join(root, "daemon-"+key)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	ws, err := svc.CreateWorkspace("fixture", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	squad, err := svc.SaveSquad(domain.Squad{Name: "fixture", Members: []domain.Member{{ID: "fixture-member"}}})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := svc.CreateMission(ws.ID, "fixture", "synthetic mission", squad.ID)
	if err != nil {
		t.Fatal(err)
	}
	account, err := svc.CreateAccount("A", adapter)
	if err != nil {
		t.Fatal(err)
	}
	account, err = svc.ConnectAccount(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, ws, mission, account, out, root
}

func readCredentialRecord(t *testing.T, svc *Service, sess domain.Session, out string) credentialRecord {
	t.Helper()
	readyProcess(t, svc, sess)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var record credentialRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestServiceClosedCredentialEnvironment(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"session", "login", "authenticated-init", "fallback-init", "direct"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				svc, ws, mission, account, out, root := isolationFixture(t, adapter)
				accountHome := filepath.Join(svc.Home, "accounts", account.ID)
				if mode == "authenticated-init" || mode == "session" || mode == "login" {
					authenticateFixtureAccount(t, svc, account, "A")
				}
				var sess domain.Session
				var err error
				switch mode {
				case "session":
					sess, err = svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter})
				case "login":
					sess, err = svc.StartLogin(account.ID, "web", "test")
				case "direct":
					sess, err = svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), adapter)}}, []string{"OPENAI_API_KEY=explicit-synthetic", "UNREVIEWED_CREDENTIAL=explicit-synthetic", "BATUTA_ROLE_SKILLS=explicit-role"}, ws.Root)
				default:
					sess, err = svc.StartInit(ws.ID, "synthetic init", "web", "test", confirmedFixtureInput(account.ID))
				}
				if mode == "fallback-init" {
					if code, ok := ReadinessFailure(err); !ok || code != "credential_missing" {
						t.Fatalf("init without authentication: %v", err)
					}
					if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("unbound init executed: %v", err)
					}
					rows, err := svc.Store.ListSessions()
					if err != nil || len(rows) != 0 {
						t.Fatalf("unbound init persisted: %v %v", rows, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				record := readCredentialRecord(t, svc, sess, out)
				if !record.PTY || !slices.Equal(record.Argv, sess.Argv[1:]) {
					t.Error("native PTY or selected argv changed")
				}
				for _, key := range credentialSentinels {
					if mode == "direct" && key == "BATUTA_ROLE_SKILLS" {
						continue // Explicit application metadata is checked below.
					}
					if _, exists := record.Env[key]; exists {
						t.Errorf("daemon key inherited: %s", key)
					}
				}
				for key, expected := range fixtureRuntime(root, os.Getenv("PATH")) {
					if key == "TERM" {
						expected = "xterm-256color"
					}
					if record.Env[key] != expected {
						t.Errorf("required runtime variable changed: %s", key)
					}
				}
				if mode == "direct" {
					if record.Env["HOME"] == os.Getenv("HOME") || record.Env["HOME"] == accountHome || record.Identity != "" {
						t.Error("unbound fallback inherited an existing identity")
					}
				} else if record.Env["HOME"] != accountHome {
					t.Error("selected account home changed")
				}
				if mode != "direct" && record.Identity != "A" {
					t.Error("authenticated init did not use selected synthetic identity")
				}
				metadata := map[string]string{}
				if mode == "direct" {
					metadata["BATUTA_ROLE_SKILLS"] = "explicit-role"
				} else if mode != "login" {
					skillsRoot, err := filepath.EvalSymlinks(filepath.Join(root, "skills", "batuta"))
					if err != nil {
						t.Fatal(err)
					}
					metadata["BATUTA_CONSOLE"] = "1"
					metadata["BATUTA_WORKSPACE"] = ws.Root
					metadata["BATUTA_SKILLS"] = skillsRoot
					metadata["BATUTA_JOURNAL"] = filepath.Join(ws.Root, ".batuta", "journal")
					if mode == "session" {
						metadata["BATUTA_MISSION"] = "synthetic mission"
					} else {
						metadata["BATUTA_INIT"] = "synthetic init"
					}
				}
				for _, key := range batutaMetadataKeys {
					expected, wanted := metadata[key]
					actual, exists := record.Env[key]
					if exists != wanted || actual != expected {
						t.Errorf("explicit/ambient Batuta metadata mismatch: %s", key)
					}
				}
				if mode != "login" {
					requireSameDirectory(t, record.CWD, ws.Root)
				}
				if mode == "login" {
					requireSameDirectory(t, record.CWD, accountHome)
				}
			})
		}
	}
}

func TestAccountIsolationRejectsBeforeSpawn(t *testing.T) {
	for _, problem := range []string{"empty-home", "adapter-mismatch", "config-file", "account-symlink"} {
		t.Run(problem, func(t *testing.T) {
			svc, _, mission, account, out, root := isolationFixture(t, "codex")
			adapter := "codex"
			switch problem {
			case "empty-home":
				svc.Home = ""
			case "adapter-mismatch":
				adapter = "claude"
			case "config-file":
				path := filepath.Join(svc.Home, "accounts", account.ID, ".config")
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "account-symlink":
				home := filepath.Join(svc.Home, "accounts", account.ID)
				if err := os.RemoveAll(home); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root, home); err != nil {
					t.Fatal(err)
				}
			}
			sess, err := svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter, Argv: []string{filepath.Join(os.Getenv("PATH"), "codex")}})
			if err == nil {
				readyProcess(t, svc, sess)
				t.Error("invalid isolation allowed a child process")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Error("invalid isolation reached spawn")
			}
		})
	}
}

func TestSessionAndLoginRejectMissingOrUnsafeAccount(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"session", "login"} {
			for _, missing := range []bool{true, false} {
				t.Run(adapter+"/"+mode+"/missing="+fmt.Sprint(missing), func(t *testing.T) {
					svc, _, mission, account, out, _ := isolationFixture(t, adapter)
					account.ID = "missing-account"
					if !missing {
						account.ID = "../unsafe-account"
						if err := svc.Store.PutAccount(account); err != nil {
							t.Fatal(err)
						}
					}
					var err error
					if mode == "session" {
						_, err = svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter, Argv: []string{filepath.Join(os.Getenv("PATH"), adapter)}})
					} else {
						_, err = svc.StartLogin(account.ID, "web", "test")
					}
					if err == nil {
						t.Fatal("missing or unsafe account accepted for " + mode)
					}
					if _, err := os.Stat(out); !os.IsNotExist(err) {
						t.Fatal("invalid account reached spawn")
					}
				})
			}
		}
	}
}

func requireSameDirectory(t *testing.T, actual, expected string) {
	t.Helper()
	a, err := os.Stat(actual)
	if err != nil {
		t.Fatal(err)
	}
	e, err := os.Stat(expected)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, e) {
		t.Fatal("child cwd does not identify the requested directory")
	}
}

func TestSameProviderAccountIdentitiesStayIndependent(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			svc, ws, mission, a, out, _ := isolationFixture(t, adapter)
			b, err := svc.CreateAccount("B", adapter)
			if err != nil {
				t.Fatal(err)
			}
			b, err = svc.ConnectAccount(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var sessions []domain.Session
			for _, account := range []domain.Account{a, b} {
				home := filepath.Join(svc.Home, "accounts", account.ID)
				config := filepath.Join(home, ".codex")
				key := "CODEX_HOME"
				if adapter == "claude" {
					config, key = filepath.Join(home, ".claude"), "CLAUDE_CONFIG_DIR"
				}
				if err := os.MkdirAll(config, 0700); err != nil {
					t.Fatal(err)
				}
				authenticateFixtureAccount(t, svc, account, account.Label)
				sess, err := svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter})
				if err != nil {
					t.Fatal(err)
				}
				sessions = append(sessions, sess)
				record := readCredentialRecord(t, svc, sess, out)
				if record.Identity != account.Label || record.Env["HOME"] != home || record.Env[key] != config {
					t.Error("account selected a different identity/config home")
				}
				for _, key := range identityKeys {
					value := record.Env[key]
					if value == os.Getenv(key) {
						t.Errorf("daemon identity path inherited: %s", key)
					}
					if value != "" {
						rel, err := filepath.Rel(home, value)
						if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
							t.Errorf("account path escaped: %s", key)
						}
					}
				}
				for _, key := range []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", key} {
					info, err := os.Stat(record.Env[key])
					if err != nil {
						t.Errorf("required private directory %s: %v", key, err)
						continue
					}
					if !info.IsDir() || info.Mode().Perm() != 0700 {
						t.Errorf("account directory is not private: %s", key)
					}
				}
			}
			for i, account := range []domain.Account{a, b} {
				if _, err := svc.Session.Stop(sessions[i].ID); err != nil {
					t.Fatal(err)
				}
				login, err := svc.StartLogin(account.ID, "web", "test")
				if err != nil {
					t.Fatal(err)
				}
				record := readCredentialRecord(t, svc, login, out)
				if record.Identity != account.Label || record.Env["HOME"] != filepath.Join(svc.Home, "accounts", account.ID) {
					t.Fatal("login selected another synthetic account")
				}
				if _, err := svc.Session.Stop(login.ID); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(svc.Home, "accounts", a.ID, ".codex", "auth.json")
			if adapter == "claude" {
				file = filepath.Join(svc.Home, "accounts", a.ID, ".claude", ".credentials.json")
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			init, err := svc.StartInit(ws.ID, "synthetic init", "web", "test", confirmedFixtureInput(b.ID))
			if err != nil {
				t.Fatal(err)
			}
			record := readCredentialRecord(t, svc, init, out)
			if record.Identity != "B" || record.Env["HOME"] != filepath.Join(svc.Home, "accounts", b.ID) {
				t.Fatal("init selected identity outside its authenticated synthetic account")
			}
			if _, err := svc.Session.Stop(init.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConnectRejectsInvalidControlledIdentity(t *testing.T) {
	for _, problem := range []string{"empty-home", "relative-home", "traversal-id", "config-symlink"} {
		t.Run(problem, func(t *testing.T) {
			svc, _, _, account, _, root := isolationFixture(t, "codex")
			account.Connected = false
			switch problem {
			case "empty-home":
				svc.Home = ""
			case "relative-home":
				svc.Home = "relative-console-home"
			case "traversal-id":
				account.ID = "../escaped-account"
			case "config-symlink":
				config := filepath.Join(svc.Home, "accounts", account.ID, ".codex")
				if err := os.Remove(config); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Symlink(root, config); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.Store.PutAccount(account); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ConnectAccount(account.ID); err == nil {
				t.Error("invalid controlled identity became connected")
			}
			current, err := svc.Store.GetAccount(account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Connected {
				t.Error("failed isolation persisted connected account")
			}
		})
	}
}

func TestUnboundHomesAreFreshAndCleanedAfterStop(t *testing.T) {
	svc, ws, _, _, out, _ := isolationFixture(t, "codex")
	previous := ""
	for i := 0; i < 2; i++ {
		sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), "codex")}}, nil, ws.Root)
		if err != nil {
			t.Fatal(err)
		}
		record := readCredentialRecord(t, svc, sess, out)
		home := record.Env["HOME"]
		if home == "" || home == os.Getenv("HOME") || home == previous {
			t.Fatal("unbound session reused an existing home")
		}
		for _, key := range identityKeys {
			path := record.Env[key]
			rel, err := filepath.Rel(home, path)
			if path == "" || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				t.Errorf("unbound path escaped private home: %s", key)
				continue
			}
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Errorf("unbound directory not private: %s", key)
			}
		}
		if err := os.WriteFile(filepath.Join(record.Env["CODEX_HOME"], "auth.json"), []byte(`{"synthetic_identity":"expired-session"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Session.Stop(sess.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("completed unbound session retained its private home")
		}
		if record.Identity != "" {
			t.Error("new unbound session selected an old identity")
		}
		previous = home
	}
}

func TestPrivateSessionSetupFailureReleasesAdmission(t *testing.T) {
	svc, ws, _, account, out, root := isolationFixture(t, "codex")
	previous := os.Getenv("TMPDIR")
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)
	sess := domain.Session{ID: domain.NewID("s_"), AccountID: account.ID, Argv: []string{filepath.Join(os.Getenv("PATH"), "codex")}}
	p, err := svc.Session.StartPending(sess, nil, ws.Root, 1)
	if err == nil {
		readyProcess(t, svc, p.Snapshot())
		t.Error("private-home setup failure reached spawn")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("failed setup launched a process")
	}
	t.Setenv("TMPDIR", previous)
	sess.ID = domain.NewID("s_")
	next, err := svc.Session.StartPending(sess, nil, ws.Root, 1)
	if err != nil {
		t.Fatalf("failed setup retained admission: %v", err)
	}
	readyProcess(t, svc, next.Snapshot())
}

func TestAccountSetupKeepsExistingConsoleHomePermissions(t *testing.T) {
	svc, _, _, account, _, _ := isolationFixture(t, "codex")
	if err := os.Chmod(svc.Home, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectAccount(account.ID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(svc.Home)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatal("account setup changed permissions of the existing Console home")
	}
}

func TestInitRejectsBrokenSelectedIdentityWithoutFallback(t *testing.T) {
	svc, ws, _, account, out, _ := isolationFixture(t, "codex")
	home := filepath.Join(svc.Home, "accounts", account.ID)
	authenticateFixtureAccount(t, svc, account, "A")
	config := filepath.Join(home, ".config")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("blocking fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartInit(ws.ID, "synthetic init", "web", "test", confirmedFixtureInput(account.ID)); err == nil {
		t.Fatal("broken selected identity silently started a fallback")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("broken selected identity reached spawn")
	}
}

func TestPrivateHomeNaturalExitAndShutdown(t *testing.T) {
	for _, natural := range []bool{true, false} {
		t.Run(fmt.Sprint(natural), func(t *testing.T) {
			svc, ws, _, _, out, _ := isolationFixture(t, "codex")
			observed := make(chan domain.Session, 1)
			onExit := svc.Session.OnExit
			svc.Session.OnExit = func(s domain.Session) { onExit(s); observed <- s }
			sess, err := svc.Session.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), "codex")}}, nil, ws.Root)
			if err != nil {
				t.Fatal(err)
			}
			record := readCredentialRecord(t, svc, sess, out)
			if natural {
				if err := svc.Session.Input(sess.ID, "test", []byte("exit\n")); err != nil {
					t.Fatal(err)
				}
				select {
				case ended := <-observed:
					if ended.Process != "exited" {
						t.Fatal("natural exit not collected")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("natural exit watchdog")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := svc.Session.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(record.Env["HOME"]); !os.IsNotExist(err) {
				t.Fatal("finished natural/shutdown lifecycle retained private home")
			}
		})
	}
}

func TestFailedStartRemovesCreatedPrivateHome(t *testing.T) {
	for _, afterSpawn := range []bool{false, true} {
		t.Run(fmt.Sprint(afterSpawn), func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("TMPDIR", base)
			logs := t.TempDir()
			argv := []string{"/missing/disposable-cli"}
			if afterSpawn {
				logs = filepath.Join(logs, "missing-logs")
				argv = []string{"/bin/cat"}
			}
			m := session.New(logs)
			_, err := m.StartPending(domain.Session{ID: "failure", AccountID: "a", Argv: argv}, nil, "", 1)
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("original start error: %v", err)
			}
			entries, err := os.ReadDir(base)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("failed start retained a created private home")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrivateHomeCleanupFailureRemainsObservable(t *testing.T) {
	_, ws, _, _, out, _ := isolationFixture(t, "codex")
	m := session.New(t.TempDir())
	svc := &Service{Session: m}
	sess, err := m.Start(domain.Session{ID: domain.NewID("s_"), Argv: []string{filepath.Join(os.Getenv("PATH"), "codex")}}, nil, ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	record := readCredentialRecord(t, svc, sess, out)
	home := record.Env["HOME"]
	t.Cleanup(func() {
		_ = os.Chmod(home, 0700)
		_ = os.RemoveAll(home)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	// Native permission failure in this disposable home; QA runs unprivileged.
	if err := os.Chmod(home, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(sess.ID); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("cleanup permission failure not returned: %v", err)
	}
	if _, err := os.Lstat(home); err != nil {
		t.Fatalf("fault fixture no longer exists: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unresolved home cleanup failure lost: %v", err)
	}
}
