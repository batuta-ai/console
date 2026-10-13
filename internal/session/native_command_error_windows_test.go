package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsNativeCommandIOFixture(t *testing.T) {
	if len(os.Args) < 6 || os.Args[3] != "task43-native-command-io" {
		return
	}
	var observed windows.Handle
	if len(os.Args) >= 7 {
		observedName, err := windows.UTF16PtrFromString(os.Args[6])
		if err != nil {
			t.Fatal(err)
		}
		observed, err = windows.OpenEvent(windows.SYNCHRONIZE, false, observedName)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(observed)
	} else if os.Args[5] != "hold" {
		t.Fatal("natural exit requires an observation event")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	output, witness := os.Stdout, os.Stderr
	if os.Args[4] == "stderr" {
		output, witness = os.Stderr, os.Stdout
	}
	birth := task43ExchangeBirth{Root: fmt.Sprintf("LIFETIME-CHILD:%d:%d", os.Getpid(), uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))}
	if err := json.NewEncoder(witness).Encode(birth); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(output, "native-command-kernel-write")
	if observed != 0 {
		if status, err := windows.WaitForSingleObject(observed, windows.INFINITE); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal(status, err)
		}
	}
	if os.Args[5] == "hold" {
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(event)
		if status, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatal(status, err)
		}
	}
	code, err := strconv.Atoi(os.Args[5])
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}

func TestTask43WindowsNativeCommandRetainsActualIOCause(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, mode := range []string{"0", "44", "hold"} {
			t.Run(stream+"/"+mode, func(t *testing.T) {
				runner := task43WindowsNativeRunner(t)
				foreign := task43ForeignSibling(t)
				read, write, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := read.Close(); err != nil {
					t.Fatal(err)
				}
				defer write.Close()
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				birthReader, birthWriter := io.Pipe()
				defer birthReader.Close()
				defer birthWriter.Close()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				command := publication.Command{Executable: exe, Directory: t.TempDir(), Args: []string{"-test.run=^TestTask43WindowsNativeCommandIOFixture$", "--", "task43-native-command-io", stream, mode}}
				observedName := fmt.Sprintf("Local\\task43-native-command-io-%d-%s-%s-%s", os.Getpid(), stream, mode, filepath.Base(command.Directory))
				observedNameUTF16, err := windows.UTF16PtrFromString(observedName)
				if err != nil {
					t.Fatal(err)
				}
				observed, err := windows.CreateEvent(nil, 1, 0, observedNameUTF16)
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(observed)
				command.Args = append(command.Args, observedName)
				sink := task43ExchangeClosedPipeSink{writer: write, release: release}
				command.Observer, command.StderrObserver = sink, birthWriter
				if stream == "stderr" {
					command.Observer, command.StderrObserver = birthWriter, sink
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				type outcome struct {
					result publication.CommandResult
					err    error
				}
				completed := make(chan outcome, 1)
				go func() {
					result, err := runner.Run(ctx, command)
					completed <- outcome{result, err}
				}()
				births := make(chan task43ExchangeBirth, 1)
				birthErrors := make(chan error, 1)
				go func() {
					var birth task43ExchangeBirth
					if err := json.NewDecoder(birthReader).Decode(&birth); err != nil {
						birthErrors <- err
						return
					}
					births <- birth
				}()
				var birth task43ExchangeBirth
				select {
				case birth = <-births:
				case err := <-birthErrors:
					t.Fatal("actual IO fixture birth missing", err)
				case early := <-completed:
					t.Fatal("IO fixture returned before its birth", early)
				case <-ctx.Done():
					t.Fatal("actual IO birth timed out", ctx.Err())
				}
				root := task43ObserveLifetimeChild(t, birth.Root)
				if err := windows.SetEvent(observed); err != nil {
					t.Fatal(err)
				}
				unblock()
				ended := <-completed
				if ended.result.ExitCode != -1 || !errors.Is(ended.err, windows.ERROR_NO_DATA) || errors.Is(ended.err, context.DeadlineExceeded) {
					t.Fatal("actual observer kernel cause lost or replaced by deadline", ended.result.ExitCode, ended.err)
				}
				if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("actual IO-failed root survived return", status, err)
				}
				task43SiblingStillRunning(t, foreign)
				runner.Manager.mu.Lock()
				remaining := len(runner.Manager.probes)
				runner.Manager.mu.Unlock()
				if remaining != 0 {
					t.Fatal("IO failure retained admission", remaining)
				}
				result, err := runner.Run(context.Background(), task43WindowsNativeCommand(t, "nonzero"))
				if err != nil || result.ExitCode != 44 {
					t.Fatal("ordinary IO error poisoned admission or changed true nonzero exit", result.ExitCode, err)
				}
			})
		}
	}
}
