package session

import (
	"context"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func nativePTYObservationHandleCount(t *testing.T) uint32 {
	t.Helper()
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	if err := proc.Find(); err != nil {
		t.Fatal(err)
	}
	var count uint32
	ok, _, err := proc.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	runtime.KeepAlive(&count)
	if ok == 0 {
		t.Fatal("actual GetProcessHandleCount failed", err)
	}
	return count
}

func TestNativePTYObservationActualAvailabilityAndHandleLifetime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	warm := ObservePTY(ctx)
	if !warm.Supported || !warm.Available || warm.Code != "pty_available" || warm.Qualification.Status != "pending" || warm.Qualification.Code != "native_operation_qualification_pending" {
		t.Fatal("actual native ConPTY observation unavailable", warm)
	}
	before := nativePTYObservationHandleCount(t)
	for range 20 {
		got := ObservePTY(ctx)
		if !got.Supported || !got.Available || got.Code != "pty_available" || got.Qualification.Status != "pending" || got.Qualification.Code != "native_operation_qualification_pending" {
			t.Fatal("actual native ConPTY observation differs", got)
		}
	}
	after := nativePTYObservationHandleCount(t)
	if after != before {
		t.Fatal("actual native ConPTY observation leaked kernel handles", before, after)
	}
	t.Logf("actual ConPTY alloc/resize/close 20 observations: kernel handles before=%d after=%d", before, after)
}

func TestNativePTYObservationCancelledAllocatesNoHandles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := nativePTYObservationHandleCount(t)
	for range 20 {
		got := ObservePTY(ctx)
		if !got.Supported || got.Available || got.Code != "check_cancelled" || got.Qualification.Status != "pending" || got.Qualification.Code != "native_operation_qualification_pending" {
			t.Fatal("cancelled native ConPTY observation differs", got)
		}
	}
	after := nativePTYObservationHandleCount(t)
	if after != before {
		t.Fatal("cancelled native ConPTY observation allocated kernel handles", before, after)
	}
	t.Logf("cancelled native ConPTY 20 observations: kernel handles before=%d after=%d", before, after)
}
