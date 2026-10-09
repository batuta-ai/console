package testfixture_test

import (
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/domain"
	consolert "github.com/batuta-ai/console/internal/runtime"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestNativeScriptCreatesPrivateFiles(t *testing.T) {
	root := testfixture.PrivateHome(t)
	executable := filepath.Join(root, "actual private fixture.exe")
	body := []byte("#!/bin/sh\nprintf '%s' 'actual original script'\nexit 44\n")
	if err := testfixture.WriteScript(t, executable, body, 0700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{executable, executable + ".shell-script", executable + ".shell-config.json"} {
		file, err := consolert.OpenPrivateFile(path)
		if err != nil {
			t.Fatal(path, err)
		}
		t.Cleanup(func() { file.Close() })
		handle := windows.Handle(file.Fd())
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
			t.Fatalf("actual fixture is not regular/single-link: path=%s info=%+v err=%v", path, info, err)
		}
		sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) {
			t.Fatal("fixture has foreign owner", path, err)
		}
		control, _, err := sd.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("fixture DACL inherits", path, control, err)
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil || acl.AceCount != 1 {
			t.Fatal("fixture has broad DACL", path, err)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, 0, &ace); err != nil {
			t.Fatal(err)
		}
		const allAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user.User.Sid) || uint32(ace.Mask)&allAccess != allAccess {
			t.Fatal("fixture DACL is not CurrentSID full access", path)
		}
		t.Logf("actual fixture path=%s owner=%s protected=%v links=%d", path, owner.String(), control&windows.SE_DACL_PROTECTED != 0, info.NumberOfLinks)
	}
	actual, err := os.ReadFile(executable + ".shell-script")
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("fixture changed original script bytes", actual, err)
	}
	metadata, err := os.ReadFile(executable + ".shell-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]string
	if err := json.Unmarshal(metadata, &descriptor); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"image_sha256", executable}, {"script_sha256", executable + ".shell-script"}, {"shell_sha256", descriptor["shell"]}} {
		data, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if descriptor[pair[0]] != hex.EncodeToString(sum[:]) {
			t.Fatal("fixture descriptor digest does not bind actual source", pair)
		}
	}
}

func TestNativeScriptRefusesAdjacentAndLinkedTargets(t *testing.T) {
	for _, kind := range []string{"script", "descriptor", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			executable := filepath.Join(root, "refused.exe")
			target := executable
			if kind == "script" {
				target += ".shell-script"
			} else if kind == "descriptor" {
				target += ".shell-config.json"
			}
			original := []byte("actual-existing-sentinel")
			outside := filepath.Join(testfixture.PrivateHome(t), "outside sentinel")
			if err := os.WriteFile(outside, original, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(outside, target)
			case "hardlink":
				err = os.Link(outside, target)
			default:
				err = os.WriteFile(target, original, 0600)
			}
			if err != nil {
				t.Fatal("actual target fixture creation", err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := testfixture.WriteScript(t, executable, []byte("#!/bin/sh\nexit 99\n"), 0700); !errors.Is(err, os.ErrExist) {
				t.Fatal("native fixture did not refuse existing object", err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("refused object identity changed", err)
			}
			for _, path := range []string{target, outside} {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, original) {
					t.Fatal("refused object bytes changed", path, data, err)
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(target) {
				t.Fatal("refused fixture created adjacent objects", entries, err)
			}
		})
	}
}

func TestNativeScriptRefusesForeignParent(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "actual foreign parent")
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(name, &attributes); err != nil {
		t.Fatal("foreign object creation at birth", err)
	}
	before, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := before.Owner()
	if err != nil || owner == nil || owner.String() != "S-1-5-32-544" {
		t.Fatal("foreign-owner fixture was not created", err)
	}
	if err := testfixture.WriteScript(t, filepath.Join(root, "refused.exe"), []byte("#!/bin/sh\nexit 99\n"), 0700); !errors.Is(err, os.ErrPermission) {
		t.Fatal("native fixture accepted foreign parent", err)
	}
	after, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || before.String() != after.String() {
		t.Fatal("native fixture adopted or changed foreign parent", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("foreign parent changed after refusal", entries, err)
	}
}

func TestNativeScriptRejectsTamperedSources(t *testing.T) {
	for _, kind := range []string{"missing", "invalid", "unknown", "trailing", "script", "image_sha256", "shell_sha256", "executable", "script_path"} {
		t.Run(kind, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			executable := filepath.Join(root, "actual refusal.exe")
			if err := testfixture.WriteScript(t, executable, []byte("#!/bin/sh\nprintf 'SCRIPT-MUST-NOT-RUN'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			config := executable + ".shell-config.json"
			data, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				err = os.Remove(config)
			case "invalid":
				err = os.WriteFile(config, []byte("{"), 0600)
			case "trailing":
				err = os.WriteFile(config, append(data, []byte(" {}")...), 0600)
			case "script":
				err = os.WriteFile(executable+".shell-script", []byte("#!/bin/sh\nprintf 'SCRIPT-MUST-NOT-RUN-MODIFIED'\n"), 0600)
			default:
				var descriptor map[string]string
				if err := json.Unmarshal(data, &descriptor); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "unknown":
					descriptor["unknown"] = "actual unexpected field"
				case "executable":
					descriptor["executable"] = filepath.Join(root, "different.exe")
				case "script_path":
					descriptor["script"] = filepath.Join(root, "different-script")
				default:
					descriptor[kind] = strings.Repeat("0", 64)
				}
				data, err = json.Marshal(descriptor)
				if err == nil {
					err = os.WriteFile(config, data, 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			runner, _ := scriptNativeRunner(t, root)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := runner.Run(ctx, publication.Command{Executable: executable, Directory: root})
			if err != nil || result.ExitCode != 126 || len(result.Stdout) != 0 || len(result.Stderr) == 0 || result.StdoutTruncated || result.StderrTruncated {
				t.Fatalf("actual altered fixture executed or lost refusal: exit=%d err=%v stdout=%q stderr=%q", result.ExitCode, err, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestNativeScriptRealTTYInputAndExitTrap(t *testing.T) {
	root := testfixture.PrivateHome(t)
	executable := filepath.Join(root, "actual TTY script.exe")
	body := []byte("#!/bin/sh\ntest -t 0 && test -t 1 && test -t 2 || exit 90\ntrap 'printf \"SCRIPT-EXIT-TRAP\\n\"' EXIT\nprintf 'SCRIPT-TTY-READY\\n'\nIFS= read -r input\nprintf 'SCRIPT-INPUT:%s\\n' \"$input\"\nexit 44\n")
	if err := testfixture.WriteScript(t, executable, body, 0700); err != nil {
		t.Fatal(err)
	}
	runner, environment := scriptNativeRunner(t, root)
	pending, err := runner.Manager.StartPending(domain.Session{ID: "actual-script-tty", Argv: []string{executable}, Cols: 80, Rows: 24, Owner: "tui", OwnerClient: "actual-client"}, environment, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := runner.Manager.SubscribeTerminal("actual-script-tty")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output strings.Builder
	for !strings.Contains(output.String(), "SCRIPT-TTY-READY") {
		event, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("actual shell has no TTY: output=%q err=%v", output.String(), err)
		}
		output.Write(event.Data)
	}
	if err := runner.Manager.InputContext(ctx, "actual-script-tty", "actual-client", []byte("literal ação input\r")); err != nil {
		t.Fatal(err)
	}
	for {
		event, err := sub.Next(ctx)
		output.Write(event.Data)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("actual shell TTY did not finish", output.String(), err)
		}
	}
	if !strings.Contains(output.String(), "SCRIPT-INPUT:literal ação input") || !strings.Contains(output.String(), "SCRIPT-EXIT-TRAP") || pending.Snapshot().Process != "exited" || pending.Snapshot().EndedAt == nil {
		t.Fatal("actual shell TTY/input/trap/lifetime changed", output.String(), pending.Snapshot())
	}
	t.Logf("actual TTY shell output=%q", output.String())
}

func TestNativeScriptLifetimeChild(t *testing.T) {
	if len(os.Args) < 5 || os.Args[3] != "console-script-lifetime" {
		return
	}
	if os.Args[4] == "tree" {
		worker := exec.Command(os.Args[0], "-test.run=^TestNativeScriptLifetimeChild$", "--", "console-script-lifetime", "hold")
		worker.Env, worker.Stdout, worker.Stderr = os.Environ(), os.Stdout, os.Stderr
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		var created, exited, kernel, user windows.Filetime
		var observed error
		if err := worker.Process.WithHandle(func(handle uintptr) {
			observed = windows.GetProcessTimes(windows.Handle(handle), &created, &exited, &kernel, &user)
		}); err != nil || observed != nil {
			t.Fatal(err, observed)
		}
		fmt.Fprintf(os.Stdout, "SCRIPT-CHILD:%d:%d\n", worker.Process.Pid, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
		if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stdout, "SCRIPT-CHILD:%d:%d\nSCRIPT-TREE-READY\n", os.Getpid(), uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))
	} else if os.Args[4] != "hold" {
		t.Fatal("unknown actual lifetime mode")
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	if status, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal(status, err)
	}
	os.Exit(0)
}

type scriptLifetimeOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *scriptLifetimeOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(data)
	if strings.Contains(w.data.String(), "SCRIPT-TREE-READY") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *scriptLifetimeOutput) output() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func TestNativeScriptOwnedTreeCancellationPreservesForeign(t *testing.T) {
	for _, action := range []string{"cancel", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			root := testfixture.PrivateHome(t)
			executable := filepath.Join(root, "actual owned tree.exe")
			child, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			quote := "'" + strings.ReplaceAll(child, "'", "'\\''") + "'"
			if err := testfixture.WriteScript(t, executable, []byte("#!/bin/sh\n"+quote+" -test.run=^TestNativeScriptLifetimeChild$ -- console-script-lifetime tree\n"), 0700); err != nil {
				t.Fatal(err)
			}
			runner, environment := scriptNativeRunner(t, root)
			foreign := exec.Command(child, "-test.run=^TestNativeScriptLifetimeChild$", "--", "console-script-lifetime", "hold")
			foreign.Env = environment
			if err := foreign.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { foreign.Process.Kill(); foreign.Wait() })
			output := &scriptLifetimeOutput{ready: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result publication.CommandResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				result, err := runner.Run(ctx, publication.Command{Executable: executable, Directory: root, Observer: output})
				done <- completion{result, err}
			}()
			deadline, stop := context.WithTimeout(context.Background(), 6*time.Second)
			defer stop()
			select {
			case <-output.ready:
			case early := <-done:
				t.Fatal("actual shell tree exited before creation receipt", early, output.output())
			case <-deadline.Done():
				t.Fatal("actual shell tree birth receipt timed out", deadline.Err(), output.output())
			}
			receipts := regexp.MustCompile(`SCRIPT-CHILD:([0-9]+):([0-9]+)`).FindAllStringSubmatch(output.output(), -1)
			if len(receipts) != 2 {
				t.Fatal("actual descendant birth receipts absent", output.output())
			}
			var observed []windows.Handle
			for _, receipt := range receipts {
				pid, err := strconv.ParseUint(receipt[1], 10, 32)
				if err != nil {
					t.Fatal(err)
				}
				stamp, err := strconv.ParseUint(receipt[2], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { windows.CloseHandle(handle) })
				var created, exited, kernel, user windows.Filetime
				if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != stamp {
					t.Fatal("observed descendant birth identity changed", err)
				}
				if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
					t.Fatal("observed descendant was not running before owned termination", status, err)
				}
				observed = append(observed, handle)
			}
			if action == "cancel" {
				cancel()
			} else if err := runner.Manager.Shutdown(deadline); err != nil {
				t.Fatal(err)
			}
			select {
			case finished := <-done:
				if action == "cancel" && (!errors.Is(finished.err, context.Canceled) || finished.result.ExitCode != -1) {
					t.Fatal("cancelled shell fixture reported successful execution", finished)
				}
				if action == "shutdown" && (finished.err != nil || finished.result.ExitCode == 0) {
					t.Fatal("shutdown shell fixture lost its real outcome", finished)
				}
			case <-deadline.Done():
				t.Fatal("owned shell tree did not join cleanup", deadline.Err())
			}
			for _, handle := range observed {
				if status, err := windows.WaitForSingleObject(handle, 0); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("owned shell descendant survived joined completion", status, err)
				}
			}
			var status uint32
			var observedErr error
			if err := foreign.Process.WithHandle(func(handle uintptr) {
				status, observedErr = windows.WaitForSingleObject(windows.Handle(handle), 0)
			}); err != nil || observedErr != nil || status != uint32(windows.WAIT_TIMEOUT) {
				t.Fatal("foreign sibling was affected by fixture cancellation", status, err, observedErr)
			}
		})
	}
}
