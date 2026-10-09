package session

import (
	"context"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
		err := windows.NtQueryInformationProcess(windows.CurrentProcess(), windows.ProcessHandleInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &returned)
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
		types[entry.Handle] = name
		runtime.KeepAlive(&object)
	}
	runtime.KeepAlive(buffer)
	return types
}

func nativePTYObservationFiles(t *testing.T) map[windows.Handle]bool {
	t.Helper()
	files := make(map[windows.Handle]bool)
	for handle, name := range nativePTYObservationHandleTypes(t) {
		if name == "File" {
			files[handle] = true
		}
	}
	return files
}

func nativePTYObservationRequireSameFiles(t *testing.T, before, after map[windows.Handle]bool) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatal("actual native file handle set differs", before, after)
	}
	for handle := range before {
		if !after[handle] {
			t.Fatal("actual native file handle identity differs", before, after)
		}
	}
}

func TestNativePTYObservationActualAvailabilityAndFileHandleLifetime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	warm := ObservePTY(ctx)
	if !warm.Supported || !warm.Available || warm.Code != "pty_available" || warm.Qualification.Status != "pending" || warm.Qualification.Code != "native_operation_qualification_pending" {
		t.Fatal("actual native ConPTY observation unavailable", warm)
	}
	before := nativePTYObservationFiles(t)
	for range 20 {
		got := ObservePTY(ctx)
		if !got.Supported || !got.Available || got.Code != "pty_available" || got.Qualification.Status != "pending" || got.Qualification.Code != "native_operation_qualification_pending" {
			t.Fatal("actual native ConPTY observation differs", got)
		}
	}
	after := nativePTYObservationFiles(t)
	nativePTYObservationRequireSameFiles(t, before, after)
	t.Logf("actual ConPTY alloc/resize/close 20 observations: file handles before=%d after=%d identities=exact", len(before), len(after))
}

func TestNativePTYObservationCancelledAllocatesNoFileHandles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := nativePTYObservationFiles(t)
	for range 20 {
		got := ObservePTY(ctx)
		if !got.Supported || got.Available || got.Code != "check_cancelled" || got.Qualification.Status != "pending" || got.Qualification.Code != "native_operation_qualification_pending" {
			t.Fatal("cancelled native ConPTY observation differs", got)
		}
	}
	after := nativePTYObservationFiles(t)
	nativePTYObservationRequireSameFiles(t, before, after)
	t.Logf("cancelled native ConPTY 20 observations: file handles before=%d after=%d identities=exact", len(before), len(after))
}

func TestNativePTYObservationFileHandleOracleDetectsActualPrivatePipe(t *testing.T) {
	before := nativePTYObservationFiles(t)
	reader, writer, err := newWindowsConsolePipe(false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if reader != 0 {
			windows.CloseHandle(reader)
		}
		if writer != 0 {
			windows.CloseHandle(writer)
		}
	}()
	open := nativePTYObservationFiles(t)
	if len(open) != len(before)+2 || !open[reader] || !open[writer] {
		t.Fatal("actual pipe handles absent from physical file oracle", before, open, reader, writer)
	}
	for handle := range before {
		if !open[handle] {
			t.Fatal("pre-existing file handle disappeared", before, open)
		}
	}
	readerErr, writerErr := windows.CloseHandle(reader), windows.CloseHandle(writer)
	reader, writer = 0, 0
	if readerErr != nil || writerErr != nil {
		t.Fatal(readerErr, writerErr)
	}
	after := nativePTYObservationFiles(t)
	nativePTYObservationRequireSameFiles(t, before, after)
	t.Logf("actual private pipe counterproof: file handles before=%d open=%d after=%d identities=exact", len(before), len(open), len(after))
}
