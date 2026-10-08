package session

import (
	"encoding/binary"
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Documented SYSTEM_THREAD_INFORMATION follows each process header in the
// SystemProcessInformation snapshot. These observations confer no authority.
type task43SystemThread struct {
	Times      [3]int64
	WaitTime   uint32
	Start      uintptr
	Process    uintptr
	Thread     uintptr
	Priority   int32
	Base       int32
	Switches   uint32
	State      uint32
	WaitReason uint32
}

// phnt's ThreadLastSystemCall (21) and ThreadSuspendCount (35) are queried only.
type task43LastSyscall struct {
	FirstArgument uintptr
	Number        uint16
	Padding       [3]uint16
	WaitTime      uint64
}

func task43ObserveNativeThreadState(t *testing.T, pids map[uintptr]bool, root windows.Handle) {
	t.Helper()
	for _, process := range []windows.Handle{windows.CurrentProcess(), root} {
		var port uintptr
		err := windows.NtQueryInformationProcess(process, windows.ProcessDebugPort, unsafe.Pointer(&port), uint32(unsafe.Sizeof(port)), nil)
		t.Logf("native original process handle=%x debugPort=%x query=%v", process, port, err)
	}
	var currentInJob int32
	ok, _, callErr := windowsProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&currentInJob)))
	t.Log("observer current-process Job membership", currentInJob, "query", ok, callErr)
	if ok != 0 && currentInJob != 0 {
		var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil)
		t.Logf("observer inherited Job flags=%x query=%v", limits.BasicLimitInformation.LimitFlags, err)
	}
	native := windows.NewLazySystemDLL("ntdll.dll")
	names := map[uint16]string{}
	for _, name := range []string{"NtCreateUserProcess", "NtAlpcSendWaitReceivePort", "NtWaitForSingleObject", "NtWaitForMultipleObjects", "NtDelayExecution", "NtResumeThread", "NtCreateThreadEx", "NtWaitForDebugEvent", "NtRemoveIoCompletion", "NtRemoveIoCompletionEx", "NtWaitForWorkViaWorkerFactory", "NtWaitForAlertByThreadId", "NtWaitForKeyedEvent"} {
		proc := native.NewProc(name)
		if err := proc.Find(); err != nil {
			t.Log("syscall export observation", name, err)
			continue
		}
		bytes := unsafe.Slice((*byte)(unsafe.Pointer(proc.Addr())), 12)
		if bytes[0] == 0x4c && bytes[1] == 0x8b && bytes[2] == 0xd1 && bytes[3] == 0xb8 {
			number := binary.LittleEndian.Uint32(bytes[4:8])
			if number <= 65535 {
				names[uint16(number)] = name
				t.Logf("current native export %s syscall=%d", name, number)
			}
		} else {
			t.Log("native export is not a standard syscall stub", name)
		}
	}
	var buffer []uint64
	var returned uint32
	for size := uint32(256 << 10); size <= 16<<20; size *= 2 {
		buffer = make([]uint64, size/8)
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buffer[0]), size, &returned)
		if errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) {
			continue
		}
		if err != nil {
			t.Log("native process snapshot", err)
			return
		}
		break
	}
	available := uintptr(len(buffer) * 8)
	if returned == 0 || uintptr(returned) > available {
		t.Log("native process snapshot exceeds observation buffer", returned)
		return
	}
	available = uintptr(returned)
	headerSize := unsafe.Sizeof(windows.SYSTEM_PROCESS_INFORMATION{})
	threadSize := unsafe.Sizeof(task43SystemThread{})
	t.Log("native observation ABI bytes", headerSize, threadSize, unsafe.Sizeof(task43LastSyscall{}))
	base := unsafe.Pointer(&buffer[0])
	for offset := uintptr(0); offset+headerSize <= available; {
		process := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Add(base, offset))
		end := available
		if process.NextEntryOffset != 0 {
			end = offset + uintptr(process.NextEntryOffset)
		}
		if end > available || end < offset+headerSize {
			t.Log("invalid native process row bounds")
			return
		}
		if pids[process.UniqueProcessID] {
			t.Logf("native process=%d kernelTime=%d userTime=%d creation=%d threads=%d", process.UniqueProcessID, process.KernelTime, process.UserTime, process.CreateTime, process.NumberOfThreads)
			for i := uint32(0); i < process.NumberOfThreads; i++ {
				at := offset + headerSize + uintptr(i)*threadSize
				if at+threadSize > end {
					t.Log("native thread row exceeds process row")
					break
				}
				thread := (*task43SystemThread)(unsafe.Add(base, at))
				if thread.Process != process.UniqueProcessID || thread.Thread > 0xffffffff {
					t.Log("native thread identity differs from process row")
					break
				}
				handle, err := windows.OpenThread(windows.THREAD_QUERY_INFORMATION|windows.THREAD_GET_CONTEXT, false, uint32(thread.Thread))
				if err != nil {
					t.Log("native thread query handle", thread.Thread, err)
					continue
				}
				var last task43LastSyscall
				status, _, _ := native.NewProc("NtQueryInformationThread").Call(uintptr(handle), 21, uintptr(unsafe.Pointer(&last)), unsafe.Sizeof(last), 0)
				var suspend uint32
				suspendStatus, _, _ := native.NewProc("NtQueryInformationThread").Call(uintptr(handle), 35, uintptr(unsafe.Pointer(&suspend)), unsafe.Sizeof(suspend), 0)
				windows.CloseHandle(handle)
				t.Logf("native process=%d thread=%d state=%d waitReason=%d lastStatus=%08x syscall=%d name=%s suspendStatus=%08x suspend=%d", thread.Process, thread.Thread, thread.State, thread.WaitReason, uint32(status), last.Number, names[last.Number], uint32(suspendStatus), suspend)
			}
		}
		if process.NextEntryOffset == 0 {
			break
		}
		offset = end
	}
}
