package session

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

type task43ExchangeClosedPipeSink struct {
	writer  *os.File
	release chan struct{}
}

func (w task43ExchangeClosedPipeSink) Write(data []byte) (int, error) {
	<-w.release
	return w.writer.Write(data)
}

func TestTask43WindowsNativeExchangeStderrFailureJoins(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	sink := task43ExchangeClosedPipeSink{writer: write, release: make(chan struct{})}
	command := task43ExchangeCommand(t, "large", "stderr-failure")
	command.StderrObserver = sink
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	births, callbackDone := make(chan task43ExchangeBirth, 1), make(chan struct{})
	type outcome struct {
		result publication.CommandResult
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		result, err := runner.Exchange(ctx, command, func(_ context.Context, output io.Reader, input io.WriteCloser) error {
			defer close(callbackDone)
			birth, err := task43ExchangeReceiveBirth(output)
			if err != nil {
				return err
			}
			births <- birth
			if err := task43ExchangeWrite(input, []byte("pending")); err != nil {
				return err
			}
			_, err = task43ExchangeRead(output)
			return err
		})
		completed <- outcome{result, err}
	}()
	var birth task43ExchangeBirth
	select {
	case birth = <-births:
	case early := <-completed:
		t.Fatal("stderr fixture exited before its birth", early)
	case <-ctx.Done():
		t.Fatal("stderr fixture did not produce its birth", ctx.Err())
	}
	root := task43ObserveLifetimeChild(t, birth.Root)
	close(sink.release)
	ended := <-completed
	if ended.result.ExitCode != -1 || !errors.Is(ended.err, windows.ERROR_NO_DATA) || errors.Is(ended.err, context.DeadlineExceeded) {
		t.Fatal("stderr kernel failure waited for deadline or lost its actual cause", ended)
	}
	select {
	case <-callbackDone:
	default:
		t.Fatal("stderr failure returned before callback joined")
	}
	if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("stderr failure retained its actual process", status, err)
	}
}
