package session

import (
	"context"
	"errors"
	"io"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTask43WindowsNativeExchangeParentPipesNotInheritable(t *testing.T) {
	runner := task43WindowsNativeRunner(t)
	var flags [2]uint32
	getHandleInformation := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
	result, err := runner.Exchange(context.Background(), task43ExchangeCommand(t, "cooperative", "private-endpoints"), func(_ context.Context, output io.Reader, input io.WriteCloser) error {
		if _, err := task43ExchangeReceiveBirth(output); err != nil {
			return err
		}
		for i, endpoint := range []any{input, output} {
			file, ok := endpoint.(interface{ Fd() uintptr })
			if !ok {
				return errors.New("parent pipe has no original handle")
			}
			if ok, _, err := getHandleInformation.Call(file.Fd(), uintptr(unsafe.Pointer(&flags[i]))); ok == 0 {
				return err
			}
		}
		if flags[0]&windows.HANDLE_FLAG_INHERIT != 0 || flags[1]&windows.HANDLE_FLAG_INHERIT != 0 {
			return errors.New("original parent IPC endpoint permits caller-wide inheritance")
		}
		if err := task43ExchangeWrite(input, []byte("stop")); err != nil {
			return err
		}
		message, err := task43ExchangeRead(output)
		if err != nil || string(message) != "released" {
			return errors.Join(errors.New("private standard-handle duplication failed"), err)
		}
		return nil
	})
	if err != nil || result.ExitCode != 0 || flags[0]&windows.HANDLE_FLAG_INHERIT != 0 || flags[1]&windows.HANDLE_FLAG_INHERIT != 0 {
		t.Fatal("parent IPC handle inheritance not private", flags, result, err)
	}
}
