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

// ProcessHandleInformation's layout is qualified on the native Windows host.
// Test-only observation never closes or duplicates the borrowed process handles.
func nativePTYObservationHandleTypes(t *testing.T) map[windows.Handle]string {
	t.Helper()
	type handleEntry struct {
		Handle                                               windows.Handle
		HandleCount, PointerCount                            uintptr
		GrantedAccess, ObjectTypeIndex, Attributes, Reserved uint32
	}
	type snapshotHeader struct{ Count, Reserved uintptr }
	if unsafe.Sizeof(uintptr(0)) != 8 || unsafe.Sizeof(handleEntry{}) != 40 || unsafe.Sizeof(snapshotHeader{}) != 16 {
		t.Fatal("unqualified native handle snapshot layout")
	}
	var buffer []byte
	var returned uint32
	for size := 16 << 10; ; size *= 2 {
		if size > 1<<20 {
			t.Fatal("native process handle snapshot exceeds bound")
		}
		buffer = make([]byte, size)
		err := windows.NtQueryInformationProcess(windows.CurrentProcess(), 51, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &returned)
		if err == windows.STATUS_INFO_LENGTH_MISMATCH {
			continue
		}
		if err != nil {
			t.Fatal("actual native process handle snapshot failed", err)
		}
		break
	}
	count := (*snapshotHeader)(unsafe.Pointer(&buffer[0])).Count
	if returned < 16 || returned > uint32(len(buffer)) || count > uintptr(returned-16)/40 {
		t.Fatal("actual native handle snapshot malformed", returned, count)
	}
	entries := unsafe.Slice((*handleEntry)(unsafe.Pointer(&buffer[16])), int(count))
	query := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryObject")
	if err := query.Find(); err != nil {
		t.Fatal(err)
	}
	type unicodeHeader struct {
		Length, MaximumLength uint16
		Padding               uint32
		Buffer                uintptr
	}
	types := make(map[windows.Handle]string, len(entries))
	for _, entry := range entries {
		var object [4096]byte
		var length uint32
		status, _, _ := query.Call(uintptr(entry.Handle), 2, uintptr(unsafe.Pointer(&object[0])), uintptr(len(object)), uintptr(unsafe.Pointer(&length)))
		if uint32(status) != 0 {
			t.Fatal("actual native object type query failed", entry.Handle, windows.NTStatus(status))
		}
		u := (*unicodeHeader)(unsafe.Pointer(&object[0]))
		base := uintptr(unsafe.Pointer(&object[0]))
		if length < 16 || length > uint32(len(object)) || u.Length == 0 || u.Length%2 != 0 || u.Length > u.MaximumLength || u.Buffer < base || u.Buffer-base > uintptr(length) || uintptr(u.Length) > uintptr(length)-(u.Buffer-base) {
			t.Fatal("actual native object type malformed", entry.Handle)
		}
		offset := u.Buffer - base
		name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&object[offset])), int(u.Length)/2))
		if name == "File" {
			kind, err := windows.GetFileType(entry.Handle)
			if err != nil {
				t.Fatal("actual native file type failed", entry.Handle, err)
			}
			switch kind {
			case windows.FILE_TYPE_PIPE:
				name = "File/Pipe"
			case windows.FILE_TYPE_DISK:
				name = "File/Disk"
			case windows.FILE_TYPE_CHAR:
				name = "File/Character"
			default:
				t.Fatal("unqualified actual native file type", entry.Handle, kind)
			}
		}
		types[entry.Handle] = name
		runtime.KeepAlive(&object)
	}
	runtime.KeepAlive(buffer)
	return types
}

func TestNativePTYObservationDiagnosticObjectTypesWithoutConsole(t *testing.T) {
	before := nativePTYObservationHandleTypes(t)
	allocations := make([][]byte, 0, 16)
	for range 16 {
		block := make([]byte, 1<<20)
		for i := range block {
			block[i] = byte(i)
		}
		allocations = append(allocations, block)
	}
	after := nativePTYObservationHandleTypes(t)
	added, removed := make(map[string]int), make(map[string]int)
	for handle, name := range after {
		if before[handle] != name {
			added[name]++
		}
	}
	for handle, name := range before {
		if after[handle] != name {
			removed[name]++
		}
	}
	t.Logf("CONTROL noConPTY actual object types before=%d after=%d added=%v removed=%v", len(before), len(after), added, removed)
	runtime.KeepAlive(allocations)
}
