package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestWindowsExchangeSlowObserverFixture(t *testing.T) {
	if len(os.Args) < 4 || os.Args[3] != "slow-exchange-observer" {
		return
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	birth := task43ExchangeBirth{Root: fmt.Sprintf("LIFETIME-CHILD:%d:%d", os.Getpid(), uint64(created.HighDateTime)<<32|uint64(created.LowDateTime))}
	data, err := json.Marshal(birth)
	if err != nil {
		t.Fatal(err)
	}
	if err := task43ExchangeWrite(os.Stdout, data); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stderr, "slow observer diagnostic"); err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(os.Stdin); err != nil || len(data) != 0 {
		t.Fatal("protocol input did not close cleanly", data, err)
	}
	os.Exit(0)
}

func TestWindowsExchangeRetiredProcessAllowsSlowObserver(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	foreign := task43ForeignSibling(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	observerReader, observerWriter := io.Pipe()
	defer observerReader.Close()
	defer observerWriter.Close()
	command := publication.Command{Executable: exe, Args: []string{"-test.run=^TestWindowsExchangeSlowObserverFixture$", "--", "slow-exchange-observer"}, StderrObserver: observerWriter}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	births := make(chan task43ExchangeBirth, 1)
	contexts := make(chan context.Context, 1)
	proceed := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(proceed) }) }
	defer release()
	type outcome struct {
		result publication.CommandResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := runner.Exchange(ctx, command, func(callback context.Context, output io.Reader, _ io.WriteCloser) error {
			birth, err := task43ExchangeReceiveBirth(output)
			if err != nil {
				return err
			}
			births <- birth
			contexts <- callback
			<-proceed
			return nil
		})
		finished <- outcome{result, err}
	}()
	var birth task43ExchangeBirth
	select {
	case birth = <-births:
	case early := <-finished:
		t.Fatal("exchange ended before birth", early)
	case <-ctx.Done():
		t.Fatal("birth timed out", ctx.Err())
	}
	root := task43ObserveLifetimeChild(t, birth.Root)
	callback := <-contexts
	release()
	select {
	case <-callback.Done():
	case <-ctx.Done():
		t.Fatal("owned process did not retire", ctx.Err())
	}
	if ctx.Err() != nil {
		t.Fatal("caller canceled before process retirement", ctx.Err())
	}
	if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("callback canceled before owned root retired", status, err)
	}
	// Keep the actual observer blocked beyond the two-second process grace.
	drainWindow, endWindow := context.WithTimeout(ctx, 3*time.Second)
	defer endWindow()
	<-drainWindow.Done()
	if ctx.Err() != nil {
		t.Fatal("caller deadline expired while observer was parked", ctx.Err())
	}
	observed := make(chan string, 1)
	go func() {
		data, err := io.ReadAll(observerReader)
		if err != nil {
			observed <- "read failed: " + err.Error()
			return
		}
		observed <- string(data)
	}()
	var ended outcome
	select {
	case ended = <-finished:
	case <-ctx.Done():
		t.Fatal("exchange did not finish after observer release", ctx.Err())
	}
	if err := observerWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if text := <-observed; text != "slow observer diagnostic" || string(ended.result.Stderr) != text || ended.result.ExitCode != 0 || ended.err != nil {
		t.Fatal("retired process acquired a false protocol timeout", text, ended.result, ended.err)
	}
	task43SiblingStillRunning(t, foreign)
	if err := runner.Manager.Shutdown(ctx); err != nil {
		t.Fatal("completed exchange poisoned manager shutdown", err)
	}
}
