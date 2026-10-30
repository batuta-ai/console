//go:build windows

package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsBirthPublicationWaitsForReady(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "published.birth.json")
	name := winFlowEventName(winFlowSpec{Common: root, Fixture: "publication"}, "first-birth-ready")
	event, err := winFlowCreateEvent(name)
	winFlowCheck(t, err)
	t.Cleanup(func() { winFlowCheck(t, windows.CloseHandle(event)) })
	ptr, err := windows.UTF16PtrFromString(path)
	winFlowCheck(t, err)
	writer, err := windows.CreateFile(ptr, windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	winFlowCheck(t, err)
	t.Cleanup(func() {
		if writer != 0 {
			winFlowCheck(t, windows.CloseHandle(writer))
		}
	})
	want := []byte("{\"publication\":\"complete\"}\n")
	var written uint32
	winFlowCheck(t, windows.WriteFile(writer, want, &written, nil))
	if written != uint32(len(want)) {
		t.Fatal("publication bytes incomplete", written)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("marker must be visible before publication completes", err)
	}
	if got, ready, err := winFlowReadBirth(path, name); err != nil || ready || len(got) != 0 {
		t.Fatalf("read before completed publication: ready=%t bytes=%q err=%v", ready, got, err)
	}
	winFlowCheck(t, windows.CloseHandle(writer))
	writer = 0
	winFlowCheck(t, windows.SetEvent(event))
	got, ready, err := winFlowReadBirth(path, name)
	if err != nil || !ready || !bytes.Equal(got, want) {
		t.Fatalf("completed publication: ready=%t bytes=%q err=%v", ready, got, err)
	}
}
