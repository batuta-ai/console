package session

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTerminal struct {
	mu         sync.Mutex
	ready      *sync.Cond
	input      *os.File
	output     *os.File
	consoleIn  windows.Handle
	consoleOut windows.Handle
	console    windows.Handle
	buffer     []byte
	pumpErr    error
	pumpDone   chan struct{}
	closeOnce  sync.Once
	closeErr   error
	stop       func() error
}

// The ConPTY endpoints are synchronous, as required by its API. The host
// reader is overlapped; the host writer uses immediate byte-pipe writes.
func newWindowsConsolePipe(overlappedReader bool) (windows.Handle, windows.Handle, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, 0, err
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")")
	if err != nil {
		return 0, 0, err
	}
	name, err := windows.UTF16PtrFromString(`\\.\pipe\batuta-console-` + rand.Text())
	if err != nil {
		return 0, 0, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	flags := uint32(windows.PIPE_ACCESS_INBOUND | windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
	if overlappedReader {
		flags |= windows.FILE_FLAG_OVERLAPPED
	}
	reader, err := windows.CreateNamedPipe(name, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 64<<10, 64<<10, 0, &sa)
	if err != nil {
		return 0, 0, err
	}
	writer, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, 0, errors.Join(err, windows.CloseHandle(reader))
	}
	fail := func(err error) (windows.Handle, windows.Handle, error) {
		return 0, 0, errors.Join(err, windows.CloseHandle(writer), windows.CloseHandle(reader))
	}
	if overlappedReader {
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			return fail(err)
		}
		overlap := windows.Overlapped{HEvent: event}
		err = windows.ConnectNamedPipe(reader, &overlap)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			cancelErr := windows.CancelIoEx(reader, &overlap)
			var transferred uint32
			drainErr := windows.GetOverlappedResult(reader, &overlap, &transferred, true)
			err = errors.Join(err, cancelErr, drainErr)
		}
		closeErr := windows.CloseHandle(event)
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			return fail(errors.Join(err, closeErr))
		}
		if closeErr != nil {
			return fail(closeErr)
		}
	} else if err := windows.ConnectNamedPipe(reader, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return fail(err)
	}
	return reader, writer, nil
}

func windowsConsoleSize(cols, rows int) (windows.Coord, error) {
	if cols <= 0 || rows <= 0 || cols > 32767 || rows > 32767 {
		return windows.Coord{}, fmt.Errorf("invalid Windows terminal dimensions")
	}
	return windows.Coord{X: int16(cols), Y: int16(rows)}, nil
}

func newWindowsTerminal(cols, rows int, stop func() error) (*windowsTerminal, error) {
	size, err := windowsConsoleSize(cols, rows)
	if err != nil {
		return nil, err
	}
	inRead, inWrite, err := newWindowsConsolePipe(false)
	if err != nil {
		return nil, err
	}
	outRead, outWrite, err := newWindowsConsolePipe(true)
	if err != nil {
		return nil, errors.Join(err, windows.CloseHandle(inWrite), windows.CloseHandle(inRead))
	}
	mode := uint32(windows.PIPE_NOWAIT)
	if err := windows.SetNamedPipeHandleState(inWrite, &mode, nil, nil); err != nil {
		return nil, errors.Join(err, windows.CloseHandle(outWrite), windows.CloseHandle(outRead), windows.CloseHandle(inWrite), windows.CloseHandle(inRead))
	}
	f := &windowsTerminal{input: os.NewFile(uintptr(inWrite), "ConPTY input"), output: os.NewFile(uintptr(outRead), "ConPTY output"), consoleIn: inRead, consoleOut: outWrite, pumpDone: make(chan struct{}), stop: stop}
	f.ready = sync.NewCond(&f.mu)
	// ConPTY can produce output during creation and close, before a session
	// reader exists. This pump drains it independently with a bounded queue.
	go f.pump()
	if err := windows.CreatePseudoConsole(size, inRead, outWrite, 0, &f.console); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func (f *windowsTerminal) releaseConsoleEndpoints() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var err error
	for _, handle := range []*windows.Handle{&f.consoleIn, &f.consoleOut} {
		if *handle != 0 {
			err = errors.Join(err, windows.CloseHandle(*handle))
			*handle = 0
		}
	}
	return err
}

func (f *windowsTerminal) pump() {
	defer close(f.pumpDone)
	var data [4096]byte
	for {
		n, err := f.output.Read(data[:])
		f.mu.Lock()
		overflow := false
		if n > 0 && f.pumpErr == nil {
			if len(f.buffer)+n > terminalQueueBytes {
				f.pumpErr, overflow = ErrTerminalOutputOverflow, true
			} else {
				f.buffer = append(f.buffer, data[:n]...)
			}
		}
		if err != nil && f.pumpErr == nil {
			f.pumpErr = err
		}
		f.ready.Broadcast()
		f.mu.Unlock()
		if overflow {
			// Keep draining the console after stopping its owned clients, so its
			// final output cannot deadlock ClosePseudoConsole on older Windows.
			if stopErr := f.stop(); stopErr != nil {
				f.mu.Lock()
				f.pumpErr = errors.Join(f.pumpErr, stopErr)
				f.mu.Unlock()
			}
		}
		if err != nil {
			return
		}
	}
}

func (f *windowsTerminal) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.buffer) == 0 && f.pumpErr == nil {
		f.ready.Wait()
	}
	if len(f.buffer) == 0 {
		return 0, f.pumpErr
	}
	n := copy(data, f.buffer)
	f.buffer = f.buffer[n:]
	if len(f.buffer) == 0 {
		f.buffer = nil
	}
	return n, nil
}

func (f *windowsTerminal) Close() error {
	f.closeOnce.Do(func() {
		inputErr := f.input.Close()
		endpointErr := f.releaseConsoleEndpoints()
		f.mu.Lock()
		console := f.console
		f.console = 0
		f.mu.Unlock()
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
		// EOF joins the actual console output lifetime even on Windows24H2,
		// where ClosePseudoConsole itself can return before clients disconnect.
		<-f.pumpDone
		outputErr := f.output.Close()
		f.mu.Lock()
		pumpErr := f.pumpErr
		f.mu.Unlock()
		if errors.Is(pumpErr, io.EOF) {
			pumpErr = nil
		}
		f.closeErr = errors.Join(inputErr, endpointErr, pumpErr, outputErr)
	})
	return f.closeErr
}

func (f *windowsTerminal) Name() string                          { return f.output.Name() }
func (f *windowsTerminal) Stat() (os.FileInfo, error)            { return f.output.Stat() }
func (f *windowsTerminal) SyscallConn() (syscall.RawConn, error) { return f.input.SyscallConn() }

func (f *windowsTerminal) resize(cols, rows int) error {
	size, err := windowsConsoleSize(cols, rows)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.console == 0 {
		return os.ErrClosed
	}
	return windows.ResizePseudoConsole(f.console, size)
}

func terminalFile(f terminalPTY) *os.File { return f.(*windowsTerminal).output }

func ownedProcessID(cmd *exec.Cmd, p *ownedProcess) int {
	if p.native {
		return int(p.pid)
	}
	return cmd.Process.Pid
}

func ownedProcessExited(cmd *exec.Cmd, p *ownedProcess) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.native {
		return p.exited
	}
	return cmd.ProcessState != nil
}

func startManagedPTY(cmd *exec.Cmd, cols, rows int) (terminalPTY, *ownedProcess, error) {
	if err := validateWindowsCommand(cmd); err != nil {
		return nil, nil, err
	}
	if _, err := windowsConsoleSize(cols, rows); err != nil {
		return nil, nil, err
	}
	p, err := newWindowsJob()
	if err != nil {
		return nil, nil, err
	}
	if err := p.createAnchor(cmd); err != nil {
		return nil, nil, errors.Join(err, p.retire(nil))
	}
	f, err := newWindowsTerminal(cols, rows, p.stop)
	if err != nil {
		return nil, nil, errors.Join(err, p.retire(nil))
	}
	fail := func(err error) (terminalPTY, *ownedProcess, error) {
		return nil, nil, errors.Join(err, p.retire(nil), f.Close())
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return fail(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PARENT_PROCESS, unsafe.Pointer(&p.anchor.Process), unsafe.Sizeof(p.anchor.Process)); err != nil {
		return fail(err)
	}
	// This attribute takes the opaque HPCON value itself, rather than the
	// address of a Go HANDLE variable. Preserve its native pointer ABI.
	update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	if ok, _, err := update.Call(uintptr(unsafe.Pointer(attrs.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(f.console), unsafe.Sizeof(f.console), 0, 0); ok == 0 {
		return fail(err)
	}
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return fail(err)
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		return fail(err)
	}
	var directory *uint16
	if cmd.Dir != "" {
		directory, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			return fail(err)
		}
	}
	env, err := windowsEnvironmentBlock(cmd.Env)
	if err != nil {
		return fail(err)
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb = uint32(unsafe.Sizeof(info))
	// Null standard handles bind to ConPTY instead of the caller's redirected I/O.
	info.Flags = windows.STARTF_USESTDHANDLES
	var root windows.ProcessInformation
	if err := windows.CreateProcess(app, command, nil, nil, false, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &env[0], directory, &info.StartupInfo, &root); err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(root.Thread)
	p.mu.Lock()
	p.root, p.pid, p.native = root.Process, root.ProcessId, true
	var member int32
	ok, _, memberErr := windowsProcessInJob.Call(uintptr(p.root), uintptr(p.job), uintptr(unsafe.Pointer(&member)))
	aborted := p.signaled
	if aborted {
		// A stop before CreateProcess returned must also cover this newly born
		// suspended root; no user instruction has run or can escape the Job.
		memberErr = errors.Join(memberErr, windows.TerminateJobObject(p.job, 1))
	}
	p.mu.Unlock()
	if ok == 0 || member == 0 {
		stopErr := windows.TerminateProcess(root.Process, 1)
		_, waitErr := windows.WaitForSingleObject(root.Process, 2000)
		return fail(errors.Join(fmt.Errorf("native root did not inherit its owned Windows job: %w", memberErr), stopErr, waitErr))
	}
	if aborted {
		return fail(errors.Join(fmt.Errorf("native console startup was stopped"), memberErr))
	}
	if err := f.releaseConsoleEndpoints(); err != nil {
		return fail(err)
	}
	if _, err := windows.ResumeThread(root.Thread); err != nil {
		return fail(err)
	}
	return f, p, nil
}
