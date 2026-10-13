package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/adapters"
	"github.com/batuta-ai/console/internal/domain"
	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func nativeOriginalCLIInspect(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{}
	for _, target := range []string{path, path + ".shell-script", path + ".shell-config.json"} {
		file, err := consolert.OpenPrivateFile(target)
		if err != nil {
			t.Fatal("original constructor did not create a qualified native triple", target, err)
		}
		var info windows.ByHandleFileInformation
		infoErr := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
		sd, sdErr := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		content, readErr := io.ReadAll(file)
		if err := errors.Join(infoErr, sdErr, readErr, file.Close()); err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) || info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
			t.Fatal("original constructor produced unsafe native file", target, info, err)
		}
		control, _, err := sd.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("original constructor file DACL is not protected", target, err)
		}
		data[target] = content
	}
	if !bytes.HasPrefix(data[path], []byte("MZ")) {
		t.Fatal("original constructor image is not native")
	}
	var descriptor map[string]string
	if err := json.Unmarshal(data[path+".shell-config.json"], &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor["executable"] != path || descriptor["script"] != path+".shell-script" {
		t.Fatal("original constructor triple path binding changed")
	}
	shell, err := os.ReadFile(descriptor["shell"])
	if err != nil {
		t.Fatal(err)
	}
	for key, content := range map[string][]byte{"image_sha256": data[path], "script_sha256": data[path+".shell-script"], "shell_sha256": shell} {
		sum := sha256.Sum256(content)
		if descriptor[key] != hex.EncodeToString(sum[:]) {
			t.Fatal("original constructor triple digest differs", key)
		}
	}
}

func TestNativeOriginalSharedCLIConstructors(t *testing.T) {
	for _, kind := range []string{"admission", "command-codex", "command-claude", "isolation-codex", "isolation-claude"} {
		t.Run(kind, func(t *testing.T) {
			var svc *Service
			var account domain.Account
			var cli string
			id := "codex"
			switch kind {
			case "admission":
				f := admissionSetup(t, 1)
				svc, account = f.svc, f.account
				cli = filepath.Join(filepath.Dir(f.launches), testfixture.ExecutableName(id))
			case "command-codex", "command-claude":
				if kind == "command-claude" {
					id = "claude"
				}
				f, selected, _ := commandFixture(t, id)
				svc, account, cli = f.svc, f.account, selected
			default:
				if kind == "isolation-claude" {
					id = "claude"
				}
				s, _, _, a, _, _ := isolationFixture(t, id)
				svc, account = s, a
				cli = filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(id))
			}
			nativeOriginalCLIInspect(t, cli)
			ad, err := adapters.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			env, err := ad.IsolateEnv(svc.Home, account)
			if err != nil {
				t.Fatal(err)
			}
			runner := session.NativeRunner{Manager: svc.Session, Environment: env}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := runner.Run(ctx, publication.Command{Executable: cli, Args: []string{"--version"}, Directory: filepath.Dir(cli)})
			want := "codex-cli 0.160.0\n"
			if id == "claude" {
				want = "2.1.289 (Claude Code)\n"
			}
			if err != nil || result.ExitCode != 0 || string(result.Stdout) != want || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
				t.Fatalf("actual original constructor version: %+v err=%v", result, err)
			}
		})
	}
}

func TestNativeOriginalIsolationCLIConstructorProfile(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		t.Run(id, func(t *testing.T) {
			svc, _, _, account, out, root := isolationFixture(t, id)
			cli := filepath.Join(os.Getenv("PATH"), testfixture.ExecutableName(id))
			nativeOriginalCLIInspect(t, cli)
			authenticateFixtureAccount(t, svc, account, "native-constructor")
			ad, err := adapters.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			env, err := ad.IsolateEnv(svc.Home, account)
			if err != nil {
				t.Fatal(err)
			}
			runner := session.NativeRunner{Manager: svc.Session, Environment: env}
			args := []string{"space value", "$(literal);*", "ação 日本語", "line1\nline2"}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := runner.Run(ctx, publication.Command{Executable: cli, Args: args, Directory: root, Stdin: []byte("exit\n")})
			if err != nil || result.ExitCode != 0 || string(result.Stdout) != "PASS\n" || len(result.Stderr) != 0 || result.StdoutTruncated || result.StderrTruncated {
				t.Fatalf("actual original credential recorder: %+v err=%v", result, err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var record credentialRecord
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.Identity != "native-constructor" || record.CWD != root || !slices.Equal(record.Argv, args) || record.PTY {
				t.Fatalf("actual original recorder private identity/cwd/literalargv=%+v", record)
			}
			for _, key := range credentialSentinels {
				if record.Env[key] != "" {
					t.Fatal("original recorder inherited daemon credential", key)
				}
			}
			if record.Env["HOME"] != ad.IsolateDir(svc.Home, account.ID) {
				t.Fatal("actual recorder HOME is not selected private account", record.Env["HOME"])
			}
		})
	}
}
