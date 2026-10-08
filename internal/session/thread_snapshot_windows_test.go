package session

import (
	"encoding/binary"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func task43ObserveOriginalCreationThread(t *testing.T, original windows.ProcessInformation, output string) {
	t.Helper()
	if runtime.GOARCH != "amd64" {
		t.Log("native context observation requires the qualified AMD64 ABI")
		return
	}
	creator := uint64(0)
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(line, "native creator thread="); ok {
			var err error
			creator, err = strconv.ParseUint(value, 10, 32)
			if err != nil {
				t.Log("creator thread observation", err)
				return
			}
		}
	}
	if creator != uint64(original.ThreadId) {
		t.Logf("original creation thread=%d differs from observed creator=%d; no thread opened by ID for control", original.ThreadId, creator)
		return
	}
	// AMD64 CONTEXT_CONTROL layout matches the frozen Go1.27.1 SDK definition:
	// size1232, flags48, Rsp152 and Rip248, with mandatory 16-byte alignment.
	buffer := make([]byte, 1248)
	align := (16 - (uintptr(unsafe.Pointer(&buffer[0])) & 15)) & 15
	context := buffer[align : align+1232]
	binary.LittleEndian.PutUint32(context[48:52], 0x100001)
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	count, _, err := kernel.NewProc("SuspendThread").Call(uintptr(original.Thread))
	if uint32(count) == 0xffffffff {
		t.Log("original thread capture suspension failed", err)
		return
	}
	ok, _, contextErr := kernel.NewProc("GetThreadContext").Call(uintptr(original.Thread), uintptr(unsafe.Pointer(&context[0])))
	resumed, resumeErr := windows.ResumeThread(original.Thread)
	if resumeErr != nil {
		t.Error("resume original test thread", resumeErr)
	}
	runtime.KeepAlive(buffer)
	if ok == 0 {
		t.Log("original context capture failed", contextErr)
		return
	}
	pc, sp := uintptr(binary.LittleEndian.Uint64(context[248:256])), binary.LittleEndian.Uint64(context[152:160])
	t.Logf("original creator thread=%d suspendPrevious=%d resumePrevious=%d rip=%x rsp=%x", original.ThreadId, uint32(count), resumed, pc, sp)
	var modules [128]windows.Handle
	var needed uint32
	if err := windows.EnumProcessModulesEx(original.Process, &modules[0], uint32(unsafe.Sizeof(modules)), &needed, 3); err != nil {
		t.Log("original process module observation", err)
		return
	}
	for i := uint32(0); i < needed/uint32(unsafe.Sizeof(modules[0])) && i < 128; i++ {
		var info windows.ModuleInfo
		if err := windows.GetModuleInformation(original.Process, modules[i], &info, uint32(unsafe.Sizeof(info))); err != nil {
			t.Log("original module mapping", err)
			continue
		}
		if pc < info.BaseOfDll || pc-info.BaseOfDll >= uintptr(info.SizeOfImage) {
			continue
		}
		var name [1024]uint16
		if err := windows.GetModuleFileNameEx(original.Process, modules[i], &name[0], uint32(len(name))); err != nil {
			t.Log("original module name", err)
			return
		}
		t.Logf("original creator PC module=%s offset=%x base=%x imageSize=%x", filepath.Base(windows.UTF16ToString(name[:])), pc-info.BaseOfDll, info.BaseOfDll, info.SizeOfImage)
		return
	}
	t.Log("original creator PC does not map to an observed module")
}
