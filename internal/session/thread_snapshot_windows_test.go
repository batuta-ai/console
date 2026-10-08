package session

import (
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
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
		task43ObserveModuleSymbol(t, original.Process, pc, info, windows.UTF16ToString(name[:]))
		return
	}
	t.Log("original creator PC does not map to an observed module")
}

func task43ObserveModuleSymbol(t *testing.T, process windows.Handle, pc uintptr, module windows.ModuleInfo, image string) {
	t.Helper()
	// Only this controlled process has a symbol session. An explicit private
	// directory avoids personal symbol paths and any implicit remote lookup.
	directory, err := windows.BytePtrFromString(t.TempDir())
	if err != nil {
		t.Log("private symbol path", err)
		return
	}
	dbg := windows.NewLazySystemDLL("dbghelp.dll")
	previous, _, _ := dbg.NewProc("SymGetOptions").Call()
	dbg.NewProc("SymSetOptions").Call(previous | 0x200 | 0x80000 | 0x1000)
	defer dbg.NewProc("SymSetOptions").Call(previous)
	if ok, _, err := dbg.NewProc("SymInitialize").Call(uintptr(process), uintptr(unsafe.Pointer(directory)), 0); ok == 0 {
		t.Log("original process symbol initialization", err)
		return
	}
	defer dbg.NewProc("SymCleanup").Call(uintptr(process))
	path, err := windows.UTF16PtrFromString(image)
	if err != nil {
		t.Log("native image path", err)
		return
	}
	loaded, _, err := dbg.NewProc("SymLoadModuleExW").Call(uintptr(process), 0, uintptr(unsafe.Pointer(path)), 0, module.BaseOfDll, uintptr(module.SizeOfImage), 0, 0)
	t.Logf("native symbol module load=%x error=%v", loaded, err)
	symbol := struct {
		SizeOfStruct uint32
		TypeIndex    uint32
		Reserved     [2]uint64
		Index        uint32
		Size         uint32
		ModBase      uint64
		Flags        uint32
		Value        uint64
		Address      uint64
		Register     uint32
		Scope        uint32
		Tag          uint32
		NameLen      uint32
		MaxNameLen   uint32
		Name         [1024]byte
	}{SizeOfStruct: 88, MaxNameLen: 1024}
	var displacement uint64
	if ok, _, err := dbg.NewProc("SymFromAddr").Call(uintptr(process), pc, uintptr(unsafe.Pointer(&displacement)), uintptr(unsafe.Pointer(&symbol))); ok == 0 {
		t.Log("original PC symbol lookup", err)
	} else if symbol.NameLen <= uint32(len(symbol.Name)) {
		t.Logf("original PC nearest symbol=%s displacement=%x flags=%x address=%x", symbol.Name[:symbol.NameLen], displacement, symbol.Flags, symbol.Address)
	}
	f, err := os.Open(image)
	if err != nil {
		t.Log("native module file observation", err)
		return
	}
	defer f.Close()
	parsed, err := pe.NewFile(f)
	if err != nil {
		t.Log("native PE observation", err)
		return
	}
	header, ok := parsed.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Log("native module is not PE64")
		return
	}
	rawOffset := func(rva uint32) (int64, bool) {
		for _, section := range parsed.Sections {
			if rva >= section.VirtualAddress && rva-section.VirtualAddress < section.Size {
				return int64(section.Offset) + int64(rva-section.VirtualAddress), true
			}
		}
		return 0, false
	}
	if offset, found := rawOffset(uint32(pc - module.BaseOfDll)); found {
		var code [64]byte
		if _, err := f.ReadAt(code[:], offset-16); err == nil {
			t.Logf("native module file PC-16 code bytes=%x", code)
		}
	}
	debug := header.DataDirectory[6]
	if offset, found := rawOffset(debug.VirtualAddress); found && debug.Size <= 28*128 {
		entries := make([]byte, debug.Size)
		if _, err := f.ReadAt(entries, offset); err != nil {
			t.Log("native PE debug directory", err)
			return
		}
		for i := 0; i+28 <= len(entries); i += 28 {
			entry := entries[i : i+28]
			if binary.LittleEndian.Uint32(entry[12:16]) != 2 {
				continue
			}
			size := binary.LittleEndian.Uint32(entry[16:20])
			if size < 25 || size > 4096 {
				continue
			}
			cv := make([]byte, size)
			if _, err := f.ReadAt(cv, int64(binary.LittleEndian.Uint32(entry[24:28]))); err != nil || string(cv[:4]) != "RSDS" {
				continue
			}
			guid := fmt.Sprintf("%08X%04X%04X%X", binary.LittleEndian.Uint32(cv[4:8]), binary.LittleEndian.Uint16(cv[8:10]), binary.LittleEndian.Uint16(cv[10:12]), cv[12:20])
			t.Logf("native module CodeView pdb=%s guid=%s age=%X timestamp=%x", strings.TrimRight(string(cv[24:]), "\x00"), guid, binary.LittleEndian.Uint32(cv[20:24]), parsed.TimeDateStamp)
		}
	}
}
