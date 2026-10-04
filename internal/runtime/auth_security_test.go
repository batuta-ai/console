//go:build darwin || linux

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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type authProcessResult struct {
	Hash  string `json:"hash"`
	Size  int    `json:"size"`
	Error string `json:"error"`
	Lock  string `json:"lock"`
}

func TestAuthProcessFixture(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "console-auth-fixture" {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	if at+2 < len(os.Args) && os.Args[at+2] == "limited-write" {
		signal.Ignore(syscall.SIGXFSZ)
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 32, Max: 32}); err != nil {
			t.Fatal(err)
		}
	}
	if at+2 < len(os.Args) && os.Args[at+2] == "hold-partial" {
		path := os.Args[at+1]
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic-partial"), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil || line != "start\n" {
		t.Fatal("fixture barrier")
	}
	result := authProcessResult{}
	for i := 0; i < 16; i++ {
		token, err := LoadOrCreateAuth(os.Args[at+1])
		if err != nil {
			result.Error = err.Error()
			break
		}
		hash := sha256.Sum256([]byte(token))
		encoded := hex.EncodeToString(hash[:])
		if i > 0 && encoded != result.Hash {
			result.Error = "token changed between reads"
			break
		}
		result.Hash, result.Size = encoded, len(token)
		info, err := os.Lstat(os.Args[at+1] + ".lock")
		if err != nil {
			result.Error = err.Error()
			break
		}
		stat := info.Sys().(*syscall.Stat_t)
		identity := fmt.Sprintf("%d:%d:%d:%d:%o", stat.Dev, stat.Ino, stat.Uid, stat.Nlink, stat.Mode&07777)
		if i > 0 && result.Lock != identity {
			result.Error = "coordination inode changed"
			break
		}
		result.Lock = identity
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
}

type authProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	cancel context.CancelFunc
	stderr bytes.Buffer
	waited bool
}

func startAuthProcess(t *testing.T, path string, mode ...string) *authProcess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	p := &authProcess{cancel: cancel}
	args := append([]string{"-test.run", "^TestAuthProcessFixture$", "--", "console-auth-fixture", path}, mode...)
	p.cmd = exec.CommandContext(ctx, exe, args...)
	p.cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Dir(path), "TMPDIR=" + filepath.Dir(path)}
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
		_ = p.input.Close()
		if !p.waited {
			_ = p.cmd.Wait()
			p.waited = true
		}
	})
	if ready, err := p.output.ReadString('\n'); err != nil || ready != "ready\n" {
		cancel()
		_ = p.cmd.Wait()
		p.waited = true
		t.Fatalf("fixture ready: %v", err)
	}
	return p
}

func (p *authProcess) release(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.input, "start\n"); err != nil {
		t.Fatal(err)
	}
	if err := p.input.Close(); err != nil {
		t.Fatal(err)
	}
}

func (p *authProcess) result() (authProcessResult, error) {
	defer p.cancel()
	var result authProcessResult
	err := json.NewDecoder(p.output).Decode(&result)
	_, drainErr := io.Copy(io.Discard, p.output)
	waitErr := p.cmd.Wait()
	p.waited = true
	if err != nil {
		return result, fmt.Errorf("fixture response: %w", err)
	}
	if drainErr != nil {
		return result, drainErr
	}
	return result, waitErr
}

func TestDaemonLockRejectsLinksWithoutChangingTarget(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprint(hard), func(t *testing.T) {
			root := t.TempDir()
			target, path := filepath.Join(root, "synthetic-target"), filepath.Join(root, "consoled.lock")
			data := []byte("synthetic lock target")
			if err := os.WriteFile(target, data, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if hard {
				err = os.Link(target, path)
			} else {
				err = os.Symlink(target, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			f, err := WriteLock(path, LockInfo{PID: 123, Addr: "synthetic", Home: root})
			if f != nil {
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err == nil {
				t.Error("lock accepted a linked target")
			}
			after, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, data) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Error("lock target modified before valid ownership")
			}
		})
	}
}

func TestAuthRefusesWritableOrLinkedParentWithoutChmod(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "console")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "auth.secret")
			if linked {
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(home, alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "auth.secret")
			} else if err := os.Chmod(home, 0777); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(home)
			if err != nil {
				t.Fatal(err)
			}
			if token, err := LoadOrCreateAuth(path); err == nil || token != "" {
				t.Error("unsafe parent accepted")
			}
			after, err := os.Stat(home)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Error("parent changed")
			}
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Error("unsafe parent used for token creation")
			}
		})
	}
}

func TestResolveRefusesUnsafeHomeBeforeCreatingLogs(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "owned-target")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			home := target
			if linked {
				home = filepath.Join(root, "alias")
				if err := os.Symlink(target, home); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(target, 0777); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Resolve(home, "127.0.0.1:0"); err == nil {
				t.Error("unsafe runtime home resolved")
			}
			after, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Error("unsafe runtime home changed before validation")
			}
		})
	}
}

func TestRuntimeFileRejectsRemovedParentWithoutFallback(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "owned-parent")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := privateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	f, err := privateFileAt(dir, "coordination", unix.O_CREAT|unix.O_RDWR)
	if f != nil {
		_ = f.Close()
		t.Fatal("unlinked parent accepted")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("persistent missing-parent error changed: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("removed-parent operation created a fallback")
	}
}

func TestAuthPreservesLegacyBytesAndPrivatizesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.secret")
	data := []byte(strings.Repeat("synthetic-legacy-", 4))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	token, err := LoadOrCreateAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if token != string(data) || !os.SameFile(before, after) || after.Mode().Perm() != 0600 {
		t.Fatal("legacy token bytes/inode or private mode changed")
	}
}

func TestAuthRejectsUnsafeExistingFilesWithoutMutation(t *testing.T) {
	for _, kind := range []string{"empty", "short", "directory", "symlink", "dangling-symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "console")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			path, target := filepath.Join(home, "auth.secret"), filepath.Join(root, "synthetic-target")
			data := []byte(strings.Repeat("synthetic-target-", 4))
			if err := os.WriteFile(target, data, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "empty", "short":
				data := []byte{}
				if kind == "short" {
					data = []byte("short-synthetic")
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "dangling-symlink":
				if err := os.Symlink(filepath.Join(root, "absent-target"), path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fileBefore, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "fifo" {
				p := startAuthProcess(t, path)
				p.release(t)
				result, err := p.result()
				if err != nil || result.Error == "" || result.Hash != "" {
					t.Fatalf("FIFO must reject before watchdog, without token: %v", err)
				}
			} else if token, err := LoadOrCreateAuth(path); err == nil || token != "" {
				t.Error("unsafe existing auth accepted or rotated")
			}
			after, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, data) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Error("unrelated synthetic target changed")
			}
			fileAfter, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(fileBefore, fileAfter) || fileBefore.Mode() != fileAfter.Mode() {
				t.Error("unsafe auth entry replaced or chmodded")
			}
			if kind == "empty" || kind == "short" {
				actual, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				if kind == "short" {
					want = "short-synthetic"
				}
				if string(actual) != want {
					t.Error("invalid existing token silently rotated")
				}
			}
			if _, err := os.Lstat(filepath.Join(root, "absent-target")); !os.IsNotExist(err) {
				t.Error("dangling link target created")
			}
		})
	}
}

func TestConcurrentAuthProcessesAgreeOnOneToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.secret")
	processes := make([]*authProcess, 12)
	for i := range processes {
		processes[i] = startAuthProcess(t, path)
	}
	for _, p := range processes {
		p.release(t)
	}
	results := make([]authProcessResult, len(processes))
	for i, p := range processes {
		result, err := p.result()
		if err != nil {
			t.Fatal(err)
		}
		results[i] = result
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	final := hex.EncodeToString(hash[:])
	lockInfo, err := os.Lstat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	lockStat := lockInfo.Sys().(*syscall.Stat_t)
	lockIdentity := fmt.Sprintf("%d:%d:%d:%d:%o", lockStat.Dev, lockStat.Ino, lockStat.Uid, lockStat.Nlink, lockStat.Mode&07777)
	if lockStat.Uid != uint32(os.Geteuid()) || lockStat.Nlink != 1 || lockInfo.Mode().Perm() != 0600 {
		t.Fatal("coordination file not privately owned")
	}
	for _, result := range results {
		if result.Error != "" || result.Size != 64 || result.Hash != final || result.Lock != lockIdentity {
			t.Errorf("concurrent auth result: size=%d, error=%q, final hash matches=%t", result.Size, result.Error, result.Hash == final)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("new token is not private")
	}
}

func TestPrivateRuntimeFileKeepsNativeSecurityFlags(t *testing.T) {
	dir, err := privateDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	f, err := privateFileAt(dir, "flags", unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	status, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if status&unix.O_NONBLOCK == 0 || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("nonblocking or close-on-exec flag lost")
	}
}

func TestInterruptedAuthCreatorReleasesLockWithoutRotatingPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.secret")
	owner := startAuthProcess(t, path, "hold-partial")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := startAuthProcess(t, path)
	reader.release(t)
	if err := owner.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.result(); err == nil {
		t.Fatal("interrupted owner exited normally")
	}
	result, err := reader.result()
	if err != nil || result.Hash != "" || !strings.Contains(result.Error, "existing auth secret is incomplete") {
		t.Fatalf("interrupted creation did not fail closed after lock release: %v, %s", err, result.Error)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "synthetic-partial" || !os.SameFile(before, after) || after.Mode().Perm() != 0600 {
		t.Fatal("partial token replaced or modified")
	}
}

func TestFailedAuthWriteNeverPublishesLegacyLengthPartialToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.secret")
	creator := startAuthProcess(t, path, "limited-write")
	creator.release(t)
	result, err := creator.result()
	if err != nil || result.Error == "" || result.Hash != "" || result.Size != 0 {
		t.Fatalf("native limited write did not fail without a token: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Error("failed creation published an auth file")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.secret.lock" {
		t.Error("known failed creation left a partial file")
	}
	token, err := LoadOrCreateAuth(path)
	if err != nil || len(token) != 64 {
		t.Error("next creator accepted a legacy-length partial token")
	}
}

func TestAuthPublicationPreservesExistingFinalFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "auth.secret")
	data := []byte(strings.Repeat("synthetic-existing-", 4))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := privateDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if token, err := createAuthAt(dir, "auth.secret"); !errors.Is(err, os.ErrExist) || token != "" {
		t.Errorf("publication must refuse the existing final file: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, actual) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Error("existing final file changed")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.secret" {
		t.Error("failed publication left staging files")
	}
}

func TestAuthDoesNotAdoptInterruptedStagingFile(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, ".auth.secret.synthetic-interrupted.tmp")
	data := []byte(strings.Repeat("synthetic-staging", 4))
	if err := os.WriteFile(orphan, data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(orphan)
	if err != nil {
		t.Fatal(err)
	}
	token, err := LoadOrCreateAuth(filepath.Join(root, "auth.secret"))
	if err != nil || len(token) != 64 || token == string(data) {
		t.Error("creator adopted an interrupted staging file")
	}
	after, err := os.Stat(orphan)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, actual) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Error("creator modified the unrelated staging file")
	}
}

func TestAuthRejectsUnsafeCoordinationFileWithoutFallback(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling", "hardlink", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "auth.secret")
			lock := path + ".lock"
			target := filepath.Join(root, "synthetic-target")
			data := []byte("synthetic coordination target")
			if err := os.WriteFile(target, data, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				err = os.Symlink(target, lock)
			case "dangling":
				err = os.Symlink(filepath.Join(root, "absent-target"), lock)
			case "hardlink":
				err = os.Link(target, lock)
			case "directory":
				err = os.Mkdir(lock, 0700)
			case "fifo":
				err = unix.Mkfifo(lock, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := startAuthProcess(t, path)
			p.release(t)
			result, err := p.result()
			if err != nil || result.Error == "" || result.Hash != "" {
				t.Fatalf("unsafe coordination must reject without token/watchdog: %v", err)
			}
			after, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, data) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("coordination target changed")
			}
			for _, missing := range []string{path, filepath.Join(root, "absent-target")} {
				if _, err := os.Lstat(missing); !os.IsNotExist(err) {
					t.Fatal("unsafe coordination created fallback/target")
				}
			}
		})
	}
}
