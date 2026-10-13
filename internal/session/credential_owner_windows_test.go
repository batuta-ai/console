package session

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	consoleruntime "github.com/batuta-ai/console/internal/runtime"
	"golang.org/x/sys/windows"
)

type task43TokenStatistics struct {
	ID             windows.LUID
	Authentication windows.LUID
	Expiration     int64
	Type           uint32
	Impersonation  uint32
	Charged        uint32
	Available      uint32
	Groups         uint32
	Privileges     uint32
	Modified       windows.LUID
}

func task43TokenState(t *testing.T, token windows.Token) (task43TokenStatistics, string) {
	t.Helper()
	var state task43TokenStatistics
	var n uint32
	if err := windows.GetTokenInformation(token, windows.TokenStatistics, (*byte)(unsafe.Pointer(&state)), uint32(unsafe.Sizeof(state)), &n); err != nil {
		t.Fatal(err)
	}
	_ = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &n)
	buf := make([]byte, n)
	if len(buf) == 0 {
		t.Fatal("missing token owner")
	}
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buf[0], uint32(len(buf)), &n); err != nil {
		t.Fatal(err)
	}
	sid := *(**windows.SID)(unsafe.Pointer(&buf[0]))
	return state, sid.String()
}

func TestTask43WindowsCredentialCreatorFixture(t *testing.T) {
	if len(os.Args) < 4 || os.Args[3] != "task43-credential-owner" {
		return
	}
	file := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(file, []byte("synthetic-provider-auth"), 0600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	credential, readErr := consoleruntime.OpenPrivateFile(file)
	if readErr == nil {
		readErr = credential.Close()
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Owner           string
		User            string
		PrivateReadable bool
	}{owner.String(), user.User.Sid.String(), readErr == nil}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestTask43WindowsCredentialCreatorIsPrivate(t *testing.T) {
	home := task43SessionHome(t)
	m := New(filepath.Join(home, "logs"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	}()
	before, beforeOwner := task43TokenState(t, windows.GetCurrentProcessToken())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result, err := m.Probe(ctx, []string{exe, "-test.run=^TestTask43WindowsCredentialCreatorFixture$", "--", "task43-credential-owner"}, nil, home)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("credential creator probe: %+v %v", result, err)
	}
	var got struct {
		Owner           string
		User            string
		PrivateReadable bool
	}
	if err := json.Unmarshal(result.Output, &got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != got.User || !got.PrivateReadable {
		t.Errorf("ordinary selected-home credential creation is not private: owner=%s user=%s readable=%v", got.Owner, got.User, got.PrivateReadable)
	}
	after, afterOwner := task43TokenState(t, windows.GetCurrentProcessToken())
	if after != before || afterOwner != beforeOwner {
		t.Fatal("owned child launch changed the caller's token")
	}
}

func task43OwnerAnchorCommand(t *testing.T, home string) *exec.Cmd {
	t.Helper()
	env, selected, err := accounts.SessionEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(selected); err != nil {
			t.Error(err)
		}
	})
	return &exec.Cmd{Env: accounts.RuntimeEnvironment(os.Environ(), env), Dir: home}
}

func TestTask43WindowsAnchorHasIndependentToken(t *testing.T) {
	home := task43SessionHome(t)
	owned, err := newWindowsJob()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owned.retire(nil); err != nil {
			t.Error(err)
		}
	}()
	cmd := task43OwnerAnchorCommand(t, home)
	if err := owned.createAnchor(cmd); err != nil {
		t.Fatal(err)
	}
	var token windows.Token
	if err := windows.OpenProcessToken(owned.anchor.Process, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	child, _ := task43TokenState(t, token)
	caller, _ := task43TokenState(t, windows.GetCurrentProcessToken())
	if child.ID == caller.ID || child.Type != windows.TokenPrimary {
		t.Fatalf("anchor has no independent primary token: child=%+v caller=%+v", child, caller)
	}
}
