package limits

import (
	"fmt"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func makeImportFIFO(string) error {
	// A filesystem FIFO is not a Windows named pipe. Keep the required test
	// failing until a real nonregular/blocking replacement fixture is qualified.
	return fmt.Errorf("Windows FIFO-equivalent fixture requires native qualification; no named-pipe coverage claimed")
}

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	var count uint32
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	if err := proc.Find(); err != nil {
		t.Fatal(err)
	}
	ok, _, err := proc.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	if ok == 0 {
		t.Fatalf("GetProcessHandleCount: %v", err)
	}
	return int(count)
}
