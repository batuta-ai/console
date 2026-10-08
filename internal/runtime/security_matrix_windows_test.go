package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func task43SetDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	var flags windows.SECURITY_INFORMATION = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		flags = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func task43ObjectSecurity(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestTask43WindowsDACLMatrix(t *testing.T) {
	sid := task43CurrentSID(t).String()
	for name, dacl := range map[string]string{
		"null": "D:PNO_ACCESS_CONTROL", "empty": "D:P",
		"everyone":     "D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)",
		"deny":         "D:P(D;;FW;;;WD)(A;;FA;;;" + sid + ")",
		"unprotected":  "D:(A;;FA;;;" + sid + ")",
		"wrong-grant":  "D:P(A;;FA;;;WD)",
		"partial":      "D:P(A;;FR;;;" + sid + ")",
		"inherit-only": "D:P(A;OIIO;FA;;;" + sid + ")",
	} {
		t.Run(name, func(t *testing.T) {
			home := task43PrivateHome(t)
			path := filepath.Join(home, "auth.secret")
			before, err := LoadOrCreateAuth(path)
			if err != nil {
				t.Fatal(err)
			}
			task43SetDACL(t, path, dacl)
			security := task43ObjectSecurity(t, path)
			t.Cleanup(func() { task43SetDACL(t, path, "D:P(A;;FA;;;"+sid+")") })
			if got, err := LoadOrCreateAuth(path); got != "" || err == nil {
				t.Fatalf("unsafe DACL accepted: length=%d error=%v", len(got), err)
			}
			if after := task43ObjectSecurity(t, path); after != security {
				t.Fatal("validation rewrote the unsafe DACL")
			}
			task43SetDACL(t, path, "D:P(A;;FA;;;"+sid+")")
			after, err := os.ReadFile(path)
			if err != nil || string(after) != before {
				t.Fatal("validation changed token bytes", err)
			}
		})
	}
}

func TestTask43WindowsLockAliases(t *testing.T) {
	for _, name := range []string{"consoled.lock", "auth.secret.lock"} {
		for _, alias := range []string{"symlink", "hardlink", "directory"} {
			t.Run(name+"/"+alias, func(t *testing.T) {
				home := task43PrivateHome(t)
				target, path := filepath.Join(home, "target"), filepath.Join(home, name)
				want := []byte("untouched synthetic lock target")
				if err := os.WriteFile(target, want, 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				switch alias {
				case "symlink":
					err = os.Symlink(target, path)
				case "hardlink":
					err = os.Link(target, path)
				case "directory":
					err = os.Mkdir(path, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
				if name == "consoled.lock" {
					f, err := WriteLock(path, LockInfo{PID: 123})
					if f != nil {
						f.Close()
					}
					if f != nil || err == nil {
						t.Fatal("daemon lock adopted alias")
					}
				} else {
					got, err := LoadOrCreateAuth(filepath.Join(home, "auth.secret"))
					if got != "" || err == nil {
						t.Fatal("initialization lock adopted alias")
					}
				}
				after, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(after, want) {
					t.Fatal("lock refusal mutated target", err)
				}
				if _, err := os.Lstat(filepath.Join(home, "auth.secret")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("alias refusal published a token", err)
				}
			})
		}
	}
}

func TestTask43WindowsReadLockRefusesAliases(t *testing.T) {
	for _, alias := range []string{"symlink", "hardlink"} {
		t.Run(alias, func(t *testing.T) {
			home := task43PrivateHome(t)
			target, path := filepath.Join(home, "target"), filepath.Join(home, "consoled.lock")
			want := []byte(`{"pid":123,"addr":"synthetic","home":"synthetic"}`)
			if err := os.WriteFile(target, want, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			if alias == "symlink" {
				err = os.Symlink(target, path)
			} else {
				err = os.Link(target, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := ReadLock(path); got != (LockInfo{}) || err == nil {
				t.Fatalf("read adopted alias: %+v %v", got, err)
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(after, want) {
				t.Fatal("read refusal changed target", err)
			}
		})
	}
}

func TestTask43WindowsReadLockBounded(t *testing.T) {
	home := task43PrivateHome(t)
	path := filepath.Join(home, "consoled.lock")
	f, err := WriteLock(path, LockInfo{PID: 123})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := []byte(`{"pid":123,"home":"` + strings.Repeat("x", 256<<10) + `"}`)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLock(path); got != (LockInfo{}) || err == nil {
		t.Fatal("oversized metadata accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, want) {
		t.Fatal("bounded read changed metadata", err)
	}
}

func TestTask43WindowsAuthDoesNotAdoptStaging(t *testing.T) {
	home := task43PrivateHome(t)
	leftover := filepath.Join(home, ".auth.secret.abrupt.tmp")
	want := strings.Repeat("x", 64)
	if err := os.WriteFile(leftover, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreateAuth(filepath.Join(home, "auth.secret"))
	if err != nil || len(got) != 64 || got == want {
		t.Fatal("staging adopted", err)
	}
	after, err := os.ReadFile(leftover)
	if err != nil || string(after) != want {
		t.Fatal("unowned recovery history changed", err)
	}
}

type task43AuthResult struct {
	Hash     string `json:"hash"`
	Size     int    `json:"size"`
	Identity string `json:"identity"`
	Error    string `json:"error"`
}

func TestTask43WindowsAuthProcessFixture(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "task43-auth-fixture" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	path, mode := os.Args[at+1], os.Args[at+2]
	if mode == "hold-partial" {
		dir, err := OpenPrivateDirectory(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		f, err := windowsPrivateFileAt(dir, filepath.Base(path)+".lock", windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := windowsLockFile(f, true); err != nil {
			t.Fatal(err)
		}
		partial, err := windowsPrivateFileAt(dir, filepath.Base(path), windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_CREATE)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := partial.WriteString("synthetic-partial"); err != nil {
			t.Fatal(err)
		}
		if err := partial.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "ready")
		_, err = io.Copy(io.Discard, os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || line != "start\n" {
		t.Fatal("fixture barrier", err)
	}
	result := task43AuthResult{}
	for i := 0; i < 16; i++ {
		token, err := LoadOrCreateAuth(path)
		if err != nil {
			result.Error = err.Error()
			break
		}
		sum := sha256.Sum256([]byte(token))
		hash := hex.EncodeToString(sum[:])
		if i > 0 && hash != result.Hash {
			result.Error = "token changed"
			break
		}
		result.Hash, result.Size = hash, len(token)
		f, err := os.Open(path + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		var info windows.ByHandleFileInformation
		err = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info)
		err = errors.Join(err, f.Close())
		if err != nil {
			t.Fatal(err)
		}
		identity := fmt.Sprintf("%d:%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.NumberOfLinks)
		if i > 0 && identity != result.Identity {
			result.Error = "coordination file changed"
			break
		}
		result.Identity = identity
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
}

type task43AuthChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	cancel context.CancelFunc
	stderr bytes.Buffer
	waited bool
}

func task43StartAuthChild(t *testing.T, path, mode string) *task43AuthChild {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	p := &task43AuthChild{cancel: cancel}
	p.cmd = exec.CommandContext(ctx, exe, "-test.run=^TestTask43WindowsAuthProcessFixture$", "--", "task43-auth-fixture", path, mode)
	p.cmd.Env = []string{"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "USERPROFILE=" + filepath.Dir(path), "HOME=" + filepath.Dir(path), "TEMP=" + os.TempDir(), "TMP=" + os.TempDir()}
	p.cmd.Stderr = &p.stderr
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	p.output = bufio.NewReader(output)
	if err := p.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		p.input.Close()
		if !p.waited {
			p.cmd.Wait()
			p.waited = true
		}
	})
	if ready, err := p.output.ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("fixture ready: %v stderr=%s", err, p.stderr.String())
	}
	return p
}

func TestTask43WindowsAuthIndependentProcesses(t *testing.T) {
	path := filepath.Join(task43PrivateHome(t), "auth.secret")
	children := make([]*task43AuthChild, 8)
	for i := range children {
		children[i] = task43StartAuthChild(t, path, "create")
	}
	for _, p := range children {
		if _, err := io.WriteString(p.input, "start\n"); err != nil {
			t.Fatal(err)
		}
		p.input.Close()
	}
	var want task43AuthResult
	for i, p := range children {
		var got task43AuthResult
		err := json.NewDecoder(p.output).Decode(&got)
		_, drainErr := io.Copy(io.Discard, p.output)
		waitErr := p.cmd.Wait()
		p.waited = true
		p.cancel()
		if err != nil || drainErr != nil || waitErr != nil || got.Error != "" || got.Size != 64 {
			t.Fatalf("native creator %d: %+v error=%v drain=%v wait=%v stderr=%s", i, got, err, drainErr, waitErr, p.stderr.String())
		}
		if i > 0 && got != want {
			t.Fatal("independent creators saw different token or file identity")
		}
		want = got
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != want.Hash {
		t.Fatal("published token differs from child results")
	}
}

func TestTask43WindowsAuthHungHolderBounded(t *testing.T) {
	path := filepath.Join(task43PrivateHome(t), "auth.secret")
	p := task43StartAuthChild(t, path, "hold-partial")
	result := make(chan error, 1)
	start := time.Now()
	go func() { _, err := LoadOrCreateAuth(path); result <- err }()
	select {
	case err := <-result:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("hung lock did not return a deadline: %v", err)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatal("lock wait exceeded bound")
		}
	case <-time.After(4 * time.Second):
		p.input.Close()
		waitErr := p.cmd.Wait()
		p.waited = true
		<-result
		t.Fatalf("auth blocked on owned hung holder; release=%v", waitErr)
	}
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	p.cmd.Wait()
	p.waited = true
	if got, err := LoadOrCreateAuth(path); got != "" || err == nil {
		t.Fatal("abrupt holder's partial token was adopted")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "synthetic-partial" {
		t.Fatal("interruption silently rotated partial token", err)
	}
}
