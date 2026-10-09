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

func nativePTYObservationDiagnosticSnapshot(t *testing.T) (uint32, int, uint32, uint64) {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		t.Fatal(err)
	}
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	threads := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == windows.GetCurrentProcessId() {
			threads++
		}
	}
	closeErr := windows.CloseHandle(snapshot)
	if err != windows.ERROR_NO_MORE_FILES || closeErr != nil {
		t.Fatal("actual native thread snapshot failed", err, closeErr)
	}
	return nativePTYObservationHandleCount(t), threads, memory.NumGC, memory.HeapAlloc
}

func TestNativePTYObservationDiagnosticActualGrowth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for batch := range 31 {
		handles, threads, gc, heap := nativePTYObservationDiagnosticSnapshot(t)
		t.Logf("DIAGNOSTIC before batch=%d actualhandles=%d actualthreads=%d NumGC=%d HeapAlloc=%d", batch, handles, threads, gc, heap)
		for range 20 {
			got := ObservePTY(ctx)
			if !got.Available || got.Code != "pty_available" {
				t.Fatal(got)
			}
		}
	}
	handles, threads, gc, heap := nativePTYObservationDiagnosticSnapshot(t)
	t.Logf("DIAGNOSTIC after actualhandles=%d actualthreads=%d NumGC=%d HeapAlloc=%d", handles, threads, gc, heap)
}

func TestNativePTYObservationDiagnosticNaturalHeapControl(t *testing.T) {
	handles, threads, gc, heap := nativePTYObservationDiagnosticSnapshot(t)
	t.Logf("CONTROL noConPTY before actualhandles=%d actualthreads=%d NumGC=%d HeapAlloc=%d", handles, threads, gc, heap)
	allocations := make([][]byte, 0, 16)
	for range 16 {
		block := make([]byte, 1<<20)
		for i := range block {
			block[i] = byte(i)
		}
		allocations = append(allocations, block)
	}
	handles, threads, gc, heap = nativePTYObservationDiagnosticSnapshot(t)
	t.Logf("CONTROL noConPTY after actualhandles=%d actualthreads=%d NumGC=%d HeapAlloc=%d", handles, threads, gc, heap)
	runtime.KeepAlive(allocations)
}
