package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/batuta-ai/console/internal/accounts"
	"golang.org/x/sys/windows"
)

// WinBase.h defines ProcThreadAttributeJobList = 13, with the input bit set.
const windowsJobListAttribute = 0x0002000d

type windowsJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

type windowsJobCompletionPort struct {
	Key  uintptr
	Port windows.Handle
}

type windowsTokenStatistics struct {
	ID             windows.LUID
	Authentication windows.LUID
	Expiration     int64
	Type           uint32
	Impersonation  uint32
	Charged        uint32
	Available      uint32
	Groups         uint32
	Privileges     uint32
	Modified       windows.LUID
}

func windowsTokenState(token windows.Token) (windowsTokenStatistics, error) {
	var state windowsTokenStatistics
	var size uint32
	err := windows.GetTokenInformation(token, windows.TokenStatistics, (*byte)(unsafe.Pointer(&state)), uint32(unsafe.Sizeof(state)), &size)
	return state, err
}

// Change only the distinct primary token of a freshly created, suspended
// process, before it can execute the selected command.
func setSuspendedProcessOwner(process windows.Handle) error {
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT, &token); err != nil {
		return err
	}
	defer token.Close()
	caller := windows.GetCurrentProcessToken()
	callerState, err := windowsTokenState(caller)
	if err != nil {
		return err
	}
	childState, err := windowsTokenState(token)
	if err != nil {
		return err
	}
	if childState.Type != windows.TokenPrimary || childState.ID == callerState.ID {
		return errors.New("Windows suspended process does not have a distinct primary token")
	}
	user, err := caller.GetTokenUser()
	if err != nil {
		return err
	}
	childUser, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if !windows.EqualSid(user.User.Sid, childUser.User.Sid) {
		return errors.New("Windows suspended process user differs from the caller")
	}
	owner := struct{ SID *windows.SID }{user.User.Sid}
	if err := windows.SetTokenInformation(token, windows.TokenOwner, (*byte)(unsafe.Pointer(&owner)), uint32(unsafe.Sizeof(owner))); err != nil {
		return err
	}
	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	buf := make([]byte, size)
	if len(buf) == 0 {
		return errors.New("Windows suspended process owner is missing")
	}
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buf[0], uint32(len(buf)), &size); err != nil {
		return err
	}
	if !windows.EqualSid(user.User.Sid, *(**windows.SID)(unsafe.Pointer(&buf[0]))) {
		return errors.New("Windows suspended process owner was not set to the selected user")
	}
	after, err := windowsTokenState(caller)
	if err != nil {
		return err
	}
	if after != callerState {
		return errors.New("caller token changed while preparing the Windows suspended process")
	}
	return nil
}

func (p *ownedProcess) setAnchorOwner() error {
	return setSuspendedProcessOwner(p.anchor.Process)
}

// Authority comes from a newly created private job and original creation
// handles. Catalog PIDs and later membership observations cannot create it.
type ownedProcess struct {
	mu       sync.Mutex
	job      windows.Handle
	port     windows.Handle
	anchor   windows.ProcessInformation
	root     windows.Handle
	pid      uint32
	native   bool
	exited   bool
	exitCode uint32
	retired  bool
	signaled bool
	forced   bool
	cancel   context.CancelFunc
	err      error

	terminationObserved bool
	terminationTotal    uint32
	terminationWaiters  []windows.Handle
}

func newOwnedProcess(*exec.Cmd) *ownedProcess {
	return &ownedProcess{retired: true, err: errors.New("process was not created by the Windows job launcher")}
}

func newWindowsJob() (*ownedProcess, error) {
	p := &ownedProcess{}
	var err error
	p.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ownedProcess, error) { return nil, errors.Join(err, p.closeHandles()) }
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(p.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fail(err)
	}
	p.port, err = windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
	if err != nil {
		return fail(err)
	}
	completion := windowsJobCompletionPort{Key: 1, Port: p.port}
	if _, err = windows.SetInformationJobObject(p.job, windows.JobObjectAssociateCompletionPortInformation, uintptr(unsafe.Pointer(&completion)), uint32(unsafe.Sizeof(completion))); err != nil {
		return fail(err)
	}
	return p, nil
}

func windowsEnvironmentBlock(env []string) ([]uint16, error) {
	for _, entry := range env {
		if strings.ContainsRune(entry, 0) {
			return nil, errors.New("environment contains a NUL character")
		}
	}
	return append(utf16.Encode([]rune(strings.Join(env, "\x00"))), 0, 0), nil
}

func (p *ownedProcess) createAnchor(cmd *exec.Cmd) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	app, err := windows.UTF16PtrFromString(filepath.Join(system, "rundll32.exe"))
	if err != nil {
		return err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{filepath.Join(system, "rundll32.exe")}))
	if err != nil {
		return err
	}
	var dir *uint16
	if cmd.Dir != "" {
		dir, err = windows.UTF16PtrFromString(cmd.Dir)
		if err != nil {
			return err
		}
	}
	env, err := windowsEnvironmentBlock(cmd.Env)
	if err != nil {
		return err
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	if err := attrs.Update(windowsJobListAttribute, unsafe.Pointer(&p.job), unsafe.Sizeof(p.job)); err != nil {
		return err
	}
	info := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	info.Cb = uint32(unsafe.Sizeof(info))
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	// This system image never resumes. Go's ParentProcess attribute inherits its
	// job atomically while preserving Cmd's standard handle and I/O ownership.
	// The host alone retains the job handle, so host exit also terminates the job.
	if err := windows.CreateProcess(app, command, nil, nil, false, flags, &env[0], dir, &info.StartupInfo, &p.anchor); err != nil {
		return err
	}
	return p.setAnchorOwner()
}

var windowsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func (p *ownedProcess) captureRoot(cmd *exec.Cmd) error {
	var captureErr error
	err := cmd.Process.WithHandle(func(handle uintptr) {
		process := windows.CurrentProcess()
		captureErr = windows.DuplicateHandle(process, windows.Handle(handle), process, &p.root, 0, false, windows.DUPLICATE_SAME_ACCESS)
	})
	if err != nil || captureErr != nil {
		return errors.Join(err, captureErr)
	}
	var inJob int32
	ok, _, err := windowsProcessInJob.Call(uintptr(p.root), uintptr(p.job), uintptr(unsafe.Pointer(&inJob)))
	if ok == 0 {
		return err
	}
	if inJob == 0 {
		return errors.New("created process did not inherit its private Windows job")
	}
	return nil
}

func validateWindowsCommand(cmd *exec.Cmd) error {
	if err := accounts.ValidateWindowsTemporaryEnvironment(cmd.Env); err != nil {
		return err
	}
	// CreateProcess can also consult its creator's effective GetTempPathW
	// fallback. Observe that length without calling the overflowing API.
	for _, key := range []string{"TMP", "TEMP", "USERPROFILE", "WINDIR"} {
		if path := os.Getenv(key); path != "" {
			if len(utf16.Encode([]rune(path))) > 258 {
				return fmt.Errorf("Windows creator temporary directory exceeds the native path contract: %w", os.ErrInvalid)
			}
			break
		}
	}
	return nil
}

func startOwnedCommand(cmd *exec.Cmd) (*ownedProcess, error) {
	if err := validateWindowsCommand(cmd); err != nil {
		return nil, err
	}
	p, err := newWindowsJob()
	if err != nil {
		return nil, err
	}
	if err := p.createAnchor(cmd); err != nil {
		return nil, errors.Join(err, p.retire(nil))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{ParentProcess: syscall.Handle(p.anchor.Process), CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, p.retire(nil))
	}
	if err := p.captureRoot(cmd); err != nil {
		cleanupErr := p.retire(nil)
		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			waitErr = nil
		}
		return nil, errors.Join(err, cleanupErr, waitErr)
	}
	return p, nil
}

func (p *ownedProcess) signalLocked() error {
	if p.retired || p.signaled {
		return p.err
	}
	if !p.terminationObserved {
		p.terminationObserved = true
		p.err = errors.Join(p.err, p.observeTermination())
	}
	if err := windows.TerminateJobObject(p.job, 1); err != nil {
		p.err = errors.Join(p.err, fmt.Errorf("terminate owned Windows job: %w", err))
		return p.err
	}
	p.signaled = true
	return p.err
}

func requestProbeStop(p *ownedProcess) error {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil
	}
	return p.stop()
}

func (p *ownedProcess) stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.retired && !p.exited {
		p.forced = true
	}
	return p.signalLocked()
}

func (p *ownedProcess) drainJob() error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		var accounting windowsJobAccounting
		if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return err
		}
		if accounting.ActiveProcesses == 0 {
			return p.waitTermination(accounting.TotalProcesses, deadline)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("owned Windows job still contains %d processes: %w", accounting.ActiveProcesses, context.DeadlineExceeded)
		}
		var message uint32
		var key uintptr
		var overlapped *windows.Overlapped
		// Notifications only wake the waiter. Drain also waits for the observed
		// process objects, since notifications are not guaranteed for every exit.
		if err := windows.GetQueuedCompletionStatus(p.port, &message, &key, &overlapped, 100); err != nil && !errors.Is(err, windows.WAIT_TIMEOUT) {
			return err
		}
	}
}

func (p *ownedProcess) closeHandles() error {
	var err error
	for _, handle := range p.terminationWaiters {
		err = errors.Join(err, windows.CloseHandle(handle))
	}
	p.terminationWaiters = nil
	for _, handle := range []*windows.Handle{&p.root, &p.anchor.Thread, &p.anchor.Process, &p.port, &p.job} {
		if *handle != 0 {
			err = errors.Join(err, windows.CloseHandle(*handle))
			*handle = 0
		}
	}
	return err
}

func (p *ownedProcess) retire(observeErr error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retired {
		return errors.Join(observeErr, p.err)
	}
	var remainingErr error
	if p.native && p.exited {
		var accounting windowsJobAccounting
		remainingErr = windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		if remainingErr == nil && accounting.ActiveProcesses != 0 {
			p.forced = true
		}
	}
	_ = p.signalLocked()
	drainErr := p.drainJob()
	p.retired = true
	p.err = errors.Join(observeErr, p.err, remainingErr, drainErr, p.closeHandles())
	return p.err
}

func waitOwned(cmd *exec.Cmd, p *ownedProcess) error {
	cleanupErr, waitErr := waitOwnedOutcome(cmd, p)
	return errors.Join(cleanupErr, waitErr)
}

func waitOwnedOutcome(cmd *exec.Cmd, p *ownedProcess) (error, error) {
	status, observeErr := windows.WaitForSingleObject(p.root, windows.INFINITE)
	if observeErr == nil && status != windows.WAIT_OBJECT_0 {
		observeErr = fmt.Errorf("owned root exit returned wait status %d", status)
	}
	if p.native {
		if observeErr == nil {
			observeErr = windows.GetExitCodeProcess(p.root, &p.exitCode)
		}
		p.mu.Lock()
		p.exited = observeErr == nil && status == windows.WAIT_OBJECT_0
		p.mu.Unlock()
		return p.retire(observeErr), nil
	}
	cleanupErr := p.retire(observeErr)
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		waitErr = nil
	}
	return cleanupErr, waitErr
}
