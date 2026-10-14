package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
)

func installedSkillFixture(t *testing.T, collection string, names ...string) string {
	t.Helper()
	for _, name := range names {
		dir := filepath.Join(collection, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: synthetic fixture\n---\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if name == "batuta" {
			if err := os.MkdirAll(filepath.Join(dir, "adapters"), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	return filepath.Join(collection, "batuta")
}

func nativeSkillFixture(t *testing.T, adapter string) (string, []string, string) {
	t.Helper()
	collection := t.TempDir()
	installedSkillFixture(t, collection, "batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected", "custom-unrelated")
	t.Setenv("BATUTA_SKILLS", collection)
	ad, err := adapters.Get(adapter)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ad.IsolateEnv(testfixture.PrivateHome(t), domain.Account{ID: "selected", Adapter: adapter})
	if err != nil {
		t.Fatal(err)
	}
	var home string
	for _, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			home = strings.TrimPrefix(entry, "HOME=")
		}
	}
	path := filepath.Join(home, ".agents", "skills")
	if adapter == "claude" {
		path = filepath.Join(home, ".claude", "skills")
	}
	return collection, env, path
}

func TestNativeSkillPreparation(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			collection, env, native := nativeSkillFixture(t, adapter)
			ws := t.TempDir()
			for range 2 {
				p, err := prepareSessionSkills(ws, true, adapter, env, []string{"custom-selected"})
				if err != nil || !p.Enabled {
					t.Fatalf("prepare=%+v %v", p, err)
				}
				argv := testfixture.Argv(t, []string{"/bin/sh", "-c", `unset BATUTA_SKILLS; for name in batuta batuta-init batuta-plan batuta-review custom-selected; do test -f "$1/$name/SKILL.md" || exit 41; done; test -d "$1/batuta/adapters" || exit 42; test ! -e "$1/custom-unrelated" || exit 43`, "native-discovery", native})
				cmd := exec.Command(argv[0], argv[1:]...)
				cmd.Env = testfixture.TerminalEnvironment(t, append(slices.Clone(env), "PATH=/usr/bin:/bin"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("native subprocess discovery: %v %s", err, out)
				}
				for _, name := range []string{"batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected"} {
					target, err := filepath.EvalSymlinks(filepath.Join(native, name))
					want, _ := filepath.EvalSymlinks(filepath.Join(collection, name))
					if err != nil || target != want {
						t.Errorf("%s -> %s want %s: %v", name, target, want, err)
					}
				}
			}
		})
	}
}

func TestNativeSkillsRejectUnsafeDestinationsAndMissingRoles(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, kind := range []string{"symlink-parent", "symlink-skills", "file-skills", "conflict", "unrelated", "missing-role", "traversal-role", "source-changed", "selection-changed"} {
			t.Run(adapter+"/"+kind, func(t *testing.T) {
				_, env, native := nativeSkillFixture(t, adapter)
				sentinel := t.TempDir()
				if err := os.WriteFile(filepath.Join(sentinel, "keep"), []byte("caller-owned"), 0600); err != nil {
					t.Fatal(err)
				}
				roles := []string{"custom-selected"}
				switch kind {
				case "symlink-parent":
					parent := filepath.Dir(native)
					// Claude's config directory exists; replace only this empty fixture directory.
					if adapter == "claude" {
						if err := os.Remove(parent); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(sentinel, parent); err != nil {
						t.Fatal(err)
					}
				case "missing-role":
					roles = []string{"missing"}
				case "traversal-role":
					roles = []string{"../custom-unrelated"}
				case "source-changed", "selection-changed":
					if _, err := prepareSessionSkills(t.TempDir(), true, adapter, env, roles); err != nil {
						t.Fatal(err)
					}
					if kind == "selection-changed" {
						roles = nil
					} else {
						next := t.TempDir()
						installedSkillFixture(t, next, "batuta", "batuta-init", "batuta-plan", "batuta-review", "custom-selected")
						t.Setenv("BATUTA_SKILLS", next)
					}
				default:
					if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "symlink-skills":
						if err := os.Symlink(sentinel, native); err != nil {
							t.Fatal(err)
						}
					case "file-skills":
						if err := os.WriteFile(native, []byte("caller-owned"), 0600); err != nil {
							t.Fatal(err)
						}
					default:
						if err := os.Mkdir(native, 0700); err != nil {
							t.Fatal(err)
						}
						name := "batuta"
						if kind == "unrelated" {
							name = "caller-skill"
						}
						if err := os.Symlink(sentinel, filepath.Join(native, name)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if p, err := prepareSessionSkills(t.TempDir(), true, adapter, env, roles); err == nil {
					t.Fatalf("unsafe preparation accepted: %+v", p)
				}
				data, err := os.ReadFile(filepath.Join(sentinel, "keep"))
				if err != nil || string(data) != "caller-owned" {
					t.Fatal("caller data changed")
				}
				entries, err := os.ReadDir(sentinel)
				if err != nil || len(entries) != 1 {
					t.Fatal("wrote through symlinked parent")
				}
			})
		}
	}
}

func TestNativeSkillsOffDoesNotPrepare(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			_, env, native := nativeSkillFixture(t, adapter)
			p, err := prepareSessionSkills(t.TempDir(), false, adapter, env, []string{"missing"})
			if err != nil || p.Enabled {
				t.Fatalf("off: %+v %v", p, err)
			}
			if _, err := os.Lstat(native); !os.IsNotExist(err) {
				t.Fatalf("off prepared native path: %v", err)
			}
		})
	}
}

func TestServiceSkillsRejectBeforeSpawn(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, kind := range []string{"missing-source", "missing-role", "conflict", "symlink-parent"} {
			t.Run(adapter+"/"+kind, func(t *testing.T) {
				svc := testSvc(t)
				collection := t.TempDir()
				installedSkillFixture(t, collection, "batuta", "batuta-plan")
				t.Setenv("BATUTA_SKILLS", collection)
				ws, err := svc.CreateWorkspace("skills", t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				account, err := svc.CreateAccount("selected", adapter)
				if err != nil {
					t.Fatal(err)
				}
				roles := []string{"batuta-plan"}
				if kind == "missing-role" {
					roles = []string{"missing"}
				}
				squad, err := svc.SaveSquad(domain.Squad{Name: "skills", Members: []domain.Member{{CLI: adapter, AccountID: account.ID, Maestro: true, Skills: roles}}})
				if err != nil {
					t.Fatal(err)
				}
				mission, err := svc.CreateMission(ws.ID, "skills", "goal", squad.ID)
				if err != nil {
					t.Fatal(err)
				}
				native := filepath.Join(svc.Home, "accounts", account.ID, ".agents", "skills")
				if adapter == "claude" {
					native = filepath.Join(svc.Home, "accounts", account.ID, ".claude", "skills")
				}
				if kind == "missing-source" {
					t.Setenv("BATUTA_SKILLS", filepath.Join(collection, "missing"))
				}
				if kind == "conflict" {
					if err := os.MkdirAll(filepath.Join(native, "batuta"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "symlink-parent" {
					if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(t.TempDir(), native); err != nil {
						t.Fatal(err)
					}
				}
				before, err := svc.Store.EventsSince(0, 1000)
				if err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(ws.Root, "accepted-child")
				sess, err := svc.StartSession(StartSessionIn{MissionID: mission.ID, Adapter: adapter, Argv: []string{"/bin/sh", "-c", `printf accepted > "$1"`, "fixture", marker}})
				if err == nil {
					svc.StopSession(sess.ID)
					t.Fatalf("invalid skills accepted: %+v", sess)
				}
				if !strings.Contains(err.Error(), "skill") {
					t.Fatalf("expected recoverable skill error before spawn, got %v", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("accepted child ran")
				}
				rows, err := svc.Store.ListSessions()
				if err != nil || len(rows) != 0 {
					t.Fatalf("accepted catalog row: %+v %v", rows, err)
				}
				after, err := svc.Store.EventsSince(0, 1000)
				if err != nil || len(after) != len(before) {
					t.Fatalf("accepted event: %+v %v", after, err)
				}
			})
		}
	}
}

func TestServiceNativeSkillsLaunch(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"mission", "init", "off", "login"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				svc, ws, mission, account, out, root := isolationFixture(t, adapter)
				installedSkillFixture(t, filepath.Join(root, "skills"), "custom-selected", "custom-unrelated")
				authenticateFixtureAccount(t, svc, account, "selected-identity")
				account.SessionCap = 1
				if err := svc.Store.PutAccount(account); err != nil {
					t.Fatal(err)
				}
				mission.SquadSnapshot.Members[0].Skills = []string{"custom-selected"}
				mission.SquadSnapshot.Members[0].CLI = adapter
				mission.SquadSnapshot.Members[0].AccountID = account.ID
				mission.SquadSnapshot.Members[0].Maestro = true
				if err := svc.Store.PutMission(mission); err != nil {
					t.Fatal(err)
				}
				accountHome := filepath.Join(svc.Home, "accounts", account.ID)
				config, credential, settings := ".codex", "auth.json", "config.toml"
				if adapter == "claude" {
					config, credential, settings = ".claude", ".credentials.json", "settings.json"
				}
				configPath := filepath.Join(accountHome, config)
				settingsPath := filepath.Join(configPath, settings)
				if err := os.WriteFile(settingsPath, []byte("synthetic-caller-settings"), 0600); err != nil {
					t.Fatal(err)
				}
				credentialPath := filepath.Join(configPath, credential)
				before, err := os.ReadFile(credentialPath)
				if err != nil {
					t.Fatal(err)
				}
				inject := mode != "off"
				var sess domain.Session
				switch mode {
				case "init":
					sess, err = svc.StartInit(ws.ID, "synthetic answers", "web", "test", confirmedFixtureInput(account.ID))
				case "login":
					sess, err = svc.StartLogin(account.ID, "web", "test")
				default:
					sess, err = svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter, Inject: &inject})
				}
				if err != nil {
					t.Fatal(err)
				}
				record := readCredentialRecord(t, svc, sess, out)
				if record.Identity != "selected-identity" || record.Env["HOME"] != accountHome || !record.PTY || !slices.Equal(record.Argv, sess.Argv[1:]) {
					t.Fatalf("identity/env/argv/PTY changed: %+v", record)
				}
				for _, key := range credentialSentinels {
					if key != "BATUTA_ROLE_SKILLS" && record.Env[key] != "" {
						t.Errorf("inherited %s", key)
					}
				}
				want := []string{}
				if mode == "mission" || mode == "init" {
					want = append(want, "batuta", "batuta-init", "batuta-plan")
				}
				if mode == "mission" {
					want = append(want, "custom-selected")
				}
				if len(record.NativeSkills) != len(want) {
					t.Fatalf("native set=%v want=%v", record.NativeSkills, want)
				}
				for _, name := range want {
					target, _ := filepath.EvalSymlinks(filepath.Join(root, "skills", name))
					if record.NativeSkills[name] != target {
						t.Errorf("native skill %s=%q want %q", name, record.NativeSkills[name], target)
					}
				}
				if sess.BatutaInjected != (len(want) > 0) {
					t.Errorf("injection claim=%v skills=%v", sess.BatutaInjected, want)
				}
				after, err := os.ReadFile(credentialPath)
				if err != nil || !slices.Equal(before, after) {
					t.Fatal("credential changed")
				}
				data, err := os.ReadFile(settingsPath)
				if err != nil || string(data) != "synthetic-caller-settings" {
					t.Fatal("settings changed")
				}
				for _, path := range []string{credentialPath, settingsPath} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("caller file mode changed")
					}
				}
				persisted, err := svc.Store.GetSession(sess.ID)
				if err != nil || persisted.AccountID != account.ID || persisted.BatutaInjected != sess.BatutaInjected || !slices.Equal(persisted.Argv, sess.Argv) {
					t.Fatalf("catalog drift: %+v %v", persisted, err)
				}
				if mode == "init" {
					commandEvent(t, svc.Store, sess, "workspace.init", wantCommandReport("init", "prompt", "", ""))
					again, err := svc.StartInit(ws.ID, "synthetic answers", "web", "test", confirmedFixtureInput(account.ID))
					if err != nil || again.ID != sess.ID {
						t.Fatalf("init reuse changed: %+v %v", again, err)
					}
				} else if mode == "mission" || mode == "off" {
					commandEvent(t, svc.Store, sess, "session.started", wantCommandReport("interactive", "prompt", "", ""))
					if _, err := svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter, Inject: &inject}); err == nil {
						t.Fatal("capacity was bypassed")
					}
				}
			})
		}
	}
}

func TestServiceNativeSkillsCustomLaunch(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			svc, ws, mission, account, out, root := isolationFixture(t, adapter)
			installedSkillFixture(t, filepath.Join(root, "skills"), "custom-selected", "custom-unrelated")
			mission.SquadSnapshot.Members[0].Skills = []string{"custom-selected"}
			mission.SquadSnapshot.Members[0].Maestro = true
			if err := svc.Store.PutMission(mission); err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(svc.Home, "accounts", account.ID)
			config, file := ".codex", "auth.json"
			if adapter == "claude" {
				config, file = ".claude", ".credentials.json"
			}
			credential := filepath.Join(home, config, file)
			sentinel := []byte(`{"synthetic_identity":"selected-custom"}`)
			if err := os.WriteFile(credential, sentinel, 0600); err != nil {
				t.Fatal(err)
			}
			argv := []string{filepath.Join(os.Getenv("PATH"), adapter), "literal; $(no-exec)"}
			sess, err := svc.StartSession(StartSessionIn{MissionID: mission.ID, AccountID: account.ID, Adapter: adapter, Argv: argv})
			if err != nil {
				t.Fatal(err)
			}
			record := readCredentialRecord(t, svc, sess, out)
			if record.Identity != "selected-custom" || record.Env["HOME"] != home || !record.PTY || !slices.Equal(sess.Argv, argv) || !slices.Equal(record.Argv, argv[1:]) {
				t.Fatalf("custom contract: %+v", record)
			}
			if !sess.BatutaInjected || len(record.NativeSkills) != 4 || record.NativeSkills["custom-selected"] == "" || record.NativeSkills["custom-unrelated"] != "" {
				t.Fatalf("native skill set: %+v", record.NativeSkills)
			}
			for _, name := range []string{"batuta", "batuta-init", "batuta-plan", "custom-selected"} {
				want, _ := filepath.EvalSymlinks(filepath.Join(root, "skills", name))
				if record.NativeSkills[name] != want {
					t.Errorf("%s source mismatch", name)
				}
			}
			data, err := os.ReadFile(credential)
			if err != nil || !slices.Equal(data, sentinel) {
				t.Fatal("credential changed")
			}
			requireSameDirectory(t, record.CWD, ws.Root)
			commandEvent(t, svc.Store, sess, "session.started", wantCommandReport("custom", "custom_argv", "", ""))
		})
	}
}

func TestServiceInitSkillsRejectBeforeSpawn(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, kind := range []string{"missing-source", "missing-init", "conflict", "symlink-skills"} {
			t.Run(adapter+"/"+kind, func(t *testing.T) {
				svc, ws, _, account, out, root := isolationFixture(t, adapter)
				authenticateFixtureAccount(t, svc, account, "selected-init")
				native := filepath.Join(svc.Home, "accounts", account.ID, ".agents", "skills")
				if adapter == "claude" {
					native = filepath.Join(svc.Home, "accounts", account.ID, ".claude", "skills")
				}
				switch kind {
				case "missing-source":
					t.Setenv("BATUTA_SKILLS", filepath.Join(root, "missing"))
				case "missing-init":
					if err := os.Remove(filepath.Join(root, "skills", "batuta-init", "SKILL.md")); err != nil {
						t.Fatal(err)
					}
				case "conflict":
					if err := os.MkdirAll(filepath.Join(native, "batuta"), 0700); err != nil {
						t.Fatal(err)
					}
				case "symlink-skills":
					if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(t.TempDir(), native); err != nil {
						t.Fatal(err)
					}
				}
				before, err := svc.Store.EventsSince(0, 1000)
				if err != nil {
					t.Fatal(err)
				}
				sess, err := svc.StartInit(ws.ID, "synthetic answers", "web", "test", confirmedFixtureInput(account.ID))
				if err == nil {
					svc.StopSession(sess.ID)
					t.Fatalf("invalid init skills accepted: %+v", sess)
				}
				if !strings.Contains(err.Error(), "skill") {
					t.Fatalf("expected skill error, got %v", err)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("init child executed")
				}
				rows, err := svc.Store.ListSessions()
				if err != nil || len(rows) != 0 {
					t.Fatalf("accepted init catalog row: %+v %v", rows, err)
				}
				after, err := svc.Store.EventsSince(0, 1000)
				if err != nil || len(after) != len(before) {
					t.Fatalf("accepted init event: %+v %v", after, err)
				}
			})
		}
	}
}
