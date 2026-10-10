package service

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func credentialRecordInput(t *testing.T) *os.File {
	t.Helper()
	input := windows.Handle(os.Stdin.Fd())
	var before, after, output uint32
	if err := windows.GetConsoleMode(input, &before); err != nil {
		t.Fatal("credential fixture native console input", err)
	}
	if err := windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &output); err != nil {
		t.Fatal("credential fixture native console output", err)
	}
	// This child's line protocol uses literal LF, independently of console Enter.
	if err := windows.SetConsoleMode(input, windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		t.Fatal("credential fixture input mode", err)
	}
	t.Cleanup(func() {
		if err := windows.SetConsoleMode(input, before); err != nil {
			t.Error("restore credential fixture input mode", err)
		}
	})
	if err := windows.GetConsoleMode(input, &after); err != nil || after != windows.ENABLE_VIRTUAL_TERMINAL_INPUT {
		t.Fatalf("credential fixture input mode=%d err=%v", after, err)
	}
	return os.Stdin
}
