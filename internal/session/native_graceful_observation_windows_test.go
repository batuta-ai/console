package session

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTask43GraceObservationDisabled(t *testing.T) {
	for _, value := range []string{"", "0", "true"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BATUTA_WINDOWS_GRACE_OBSERVE", value)
			// A disabled observer must not even dereference the manager.
			o := task43ObserveGrace(t, nil)
			if o != nil {
				t.Fatal("disabled observation acquired resources")
			}
			o.phase("before-cancel")
			o.finish("completion")
		})
	}
}

func TestTask43GraceObservationLifecycle(t *testing.T) {
	t.Setenv("BATUTA_WINDOWS_GRACE_OBSERVE", "1")
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(job); err != nil {
			t.Error(err)
		}
	})
	var root windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &root, windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(root); err != nil {
			t.Error(err)
		}
	})
	p := &ownedProcess{job: job, root: root, pid: uint32(os.Getpid()), native: true}
	m := New(t.TempDir())
	m.probes[p] = struct{}{}
	o := task43ObserveGrace(t, m)
	if o == nil || o.job == 0 || o.root == 0 || o.captureError != "" {
		t.Fatalf("owned handles not captured: %+v", o)
	}
	jobCopy, rootCopy := o.job, o.root
	getFlags := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
	for _, h := range []windows.Handle{jobCopy, rootCopy} {
		var flags uint32
		if ok, _, err := getFlags.Call(uintptr(h), uintptr(unsafe.Pointer(&flags))); ok == 0 || flags&windows.HANDLE_FLAG_INHERIT != 0 {
			t.Fatal("observer handle is not noninheritable", flags, err)
		}
	}
	// Retirement may own this mutex throughout cleanup. Sampling/join must not wait on it.
	p.mu.Lock()
	o.phase("before-cancel")
	o.finish("completion")
	p.mu.Unlock()
	o.finish("cleanup")
	select {
	case <-o.done:
	default:
		t.Fatal("sampler was not joined")
	}
	if _, err := windows.WaitForSingleObject(rootCopy, 0); err != windows.ERROR_INVALID_HANDLE {
		t.Fatal("duplicate root survived finish", err)
	}
	var members task43GraceMembers
	if err := queryWindowsJob(jobCopy, windows.JobObjectBasicProcessIdList, unsafe.Pointer(&members), uint32(unsafe.Sizeof(members))); err != windows.ERROR_INVALID_HANDLE {
		t.Fatal("duplicate job survived finish", err)
	}
	if err := queryWindowsJob(job, windows.JobObjectBasicProcessIdList, unsafe.Pointer(&members), uint32(unsafe.Sizeof(members))); err != nil {
		t.Fatal("observer closed original job", err)
	}
	if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("observer changed original root", status, err)
	}
	if len(o.records) < 3 || o.records[0].Root.PID != uint32(os.Getpid()) || o.records[0].Root.Birth == nil || o.records[0].Root.InJob == nil || *o.records[0].Root.InJob || o.records[0].Root.ExitCode == nil || *o.records[0].Root.ExitCode != 259 {
		t.Fatalf("actual root identity/membership/status absent: %+v", o.records)
	}
	if o.records[0].Root.Wait == nil || *o.records[0].Root.Wait != uint32(windows.WAIT_TIMEOUT) || len(o.records[0].Root.Errors) != 0 || o.records[0].Incomplete || o.records[0].Listed != 0 {
		t.Fatal("kernel observation lost status or empty-job membership", o.records[0])
	}
	if o.originalJob != job || o.originalRoot != root || o.duplicateJob != jobCopy || o.duplicateRoot != rootCopy {
		t.Fatal("handle provenance was lost")
	}
	if o.records[len(o.records)-1].Owned.Available {
		t.Fatal("locked owned state was guessed available")
	}
	if len(o.closeErrors) != 0 {
		t.Fatal(o.closeErrors)
	}
}

func TestTask43GraceObservationUnavailable(t *testing.T) {
	t.Setenv("BATUTA_WINDOWS_GRACE_OBSERVE", "1")
	m := New(t.TempDir())
	m.mu.Lock()
	o := task43ObserveGrace(t, m)
	m.mu.Unlock()
	o.finish("completion")
	if o.captureError == "" || o.job != 0 || o.root != 0 {
		t.Fatalf("unavailable acquisition was not explicit: %+v", o)
	}
}

func TestTask43GraceObservationPartialAcquisition(t *testing.T) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(job); err != nil {
			t.Error(err)
		}
	})
	m := New(t.TempDir())
	// Zero is an invalid root. Capture rejects it before acquiring anything.
	p := &ownedProcess{job: job, native: true}
	m.probes[p] = struct{}{}
	o := &task43GraceObservation{}
	o.capture(m)
	if o.captureError == "" || o.job != 0 || o.root != 0 {
		t.Fatal("missing root acquired observer handles", o)
	}
	// InvalidHandle is a process pseudo-handle, so use a non-pseudo invalid value.
	p.root = windows.Handle(0x12345678)
	o = &task43GraceObservation{}
	o.capture(m)
	t.Cleanup(o.closeHandles)
	if o.captureError == "" || o.job == 0 || o.root != 0 {
		t.Fatal("root acquisition failure did not retain partial ownership", o)
	}
	duplicate := o.job
	o.closeHandles()
	var members task43GraceMembers
	if err := queryWindowsJob(duplicate, windows.JobObjectBasicProcessIdList, unsafe.Pointer(&members), uint32(unsafe.Sizeof(members))); err != windows.ERROR_INVALID_HANDLE {
		t.Fatal("partial duplicate was not closed", err)
	}
	if len(o.closeErrors) != 0 {
		t.Fatal(o.closeErrors)
	}
}

const task43GraceSampleLimit = 256

// The PID list is an actual Job query, not an accounting estimate.
type task43GraceMembers struct {
	Assigned uint32
	Count    uint32
	IDs      [32]uintptr
}

type task43GraceProcess struct {
	ListedPID uintptr
	PID       uint32
	Birth     *uint64
	InJob     *bool
	Wait      *uint32
	ExitCode  *uint32
	Errors    map[string]string
}

type task43GraceOwned struct {
	Available                bool
	PID                      uint32
	Native                   bool
	Exited                   bool
	Retired                  bool
	Signaled                 bool
	Forced                   bool
	ExitCode                 *uint32
	Error                    string
	TerminationObserved      bool
	TerminationTotalObserved bool
	TerminationTotal         uint32
}

type task43GraceRecord struct {
	Phase       string
	OffsetNS    int64
	EndOffsetNS int64
	Owned       task43GraceOwned
	Root        task43GraceProcess
	Assigned    uint32
	Listed      uint32
	Incomplete  bool
	ListError   string
	Members     []task43GraceProcess
}

type task43GraceRequest struct {
	phase string
	stop  bool
	ack   chan struct{}
}

type task43GraceObservation struct {
	t                *testing.T
	start            time.Time
	owned            *ownedProcess
	originalJob      windows.Handle
	originalRoot     windows.Handle
	duplicateJob     windows.Handle
	duplicateRoot    windows.Handle
	job              windows.Handle
	root             windows.Handle
	initial          task43GraceOwned
	captureError     string
	requests         chan task43GraceRequest
	done             chan struct{}
	once             sync.Once
	records          []task43GraceRecord
	periodicSamples  int
	periodicLimitHit bool
	closeErrors      []string
}

func task43GraceError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Caller holds p.mu. exitCode is written before the waiter's lock; read it
// only after exited publishes that write. A busy retirement stays unknown.
func task43GraceOwnedLocked(p *ownedProcess) task43GraceOwned {
	s := task43GraceOwned{
		Available: true, PID: p.pid, Native: p.native, Exited: p.exited,
		Retired: p.retired, Signaled: p.signaled, Forced: p.forced,
		Error: task43GraceError(p.err), TerminationObserved: p.terminationObserved,
		TerminationTotalObserved: p.terminationTotalObserved, TerminationTotal: p.terminationTotal,
	}
	if p.exited {
		code := p.exitCode
		s.ExitCode = &code
	}
	return s
}

func task43ObserveGrace(t *testing.T, m *Manager) *task43GraceObservation {
	if os.Getenv("BATUTA_WINDOWS_GRACE_OBSERVE") != "1" {
		return nil
	}
	o := &task43GraceObservation{t: t, start: time.Now(), requests: make(chan task43GraceRequest), done: make(chan struct{})}
	t.Cleanup(func() { o.finish("test-cleanup") })
	o.capture(m)
	if o.captureError != "" {
		o.closeHandles()
		close(o.done)
		return o
	}
	go o.sampleLoop()
	o.phase("readiness")
	return o
}

func (o *task43GraceObservation) capture(m *Manager) {
	if m == nil || !m.mu.TryLock() {
		o.captureError = "manager unavailable at readiness; no acquisition retry"
		return
	}
	var candidate *ownedProcess
	count := len(m.probes)
	if count == 1 {
		for p := range m.probes {
			candidate = p
		}
	}
	m.mu.Unlock()
	if candidate == nil {
		o.captureError = fmt.Sprintf("expected one registered probe at readiness, observed %d; registration may race readiness", count)
		return
	}
	if !candidate.mu.TryLock() {
		o.captureError = "owned state busy at readiness; exact decision unavailable"
		return
	}
	defer candidate.mu.Unlock()
	o.initial = task43GraceOwnedLocked(candidate)
	if !candidate.native || candidate.retired || candidate.job == 0 || candidate.root == 0 {
		o.captureError = "registered probe has no live owned native Job/root"
		return
	}
	o.owned, o.originalJob, o.originalRoot = candidate, candidate.job, candidate.root
	// JOB_OBJECT_QUERY=0x0004. Duplicates retain the actual owned objects but
	// extend their lifetime (including KILL_ON_JOB_CLOSE) and perturb timing.
	current := windows.CurrentProcess()
	if err := windows.DuplicateHandle(current, candidate.job, current, &o.job, 0x0004, false, 0); err != nil {
		o.captureError = "DuplicateHandle(job): " + err.Error()
		return
	}
	o.duplicateJob = o.job
	if err := windows.DuplicateHandle(current, candidate.root, current, &o.root, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, 0); err != nil {
		o.captureError = "DuplicateHandle(root): " + err.Error()
	}
	o.duplicateRoot = o.root
}

func task43GraceInspectProcess(handle, job windows.Handle, listed uintptr) task43GraceProcess {
	p := task43GraceProcess{ListedPID: listed, Errors: make(map[string]string)}
	pid, err := windows.GetProcessId(handle)
	if err != nil {
		p.Errors["GetProcessId"] = err.Error()
	} else {
		p.PID = pid
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		p.Errors["GetProcessTimes"] = err.Error()
	} else {
		birth := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
		p.Birth = &birth
	}
	var inJob int32
	if ok, _, err := windowsProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&inJob))); ok == 0 {
		p.Errors["IsProcessInJob"] = err.Error()
	} else {
		member := inJob != 0
		p.InJob = &member
	}
	if status, err := windows.WaitForSingleObject(handle, 0); err != nil {
		p.Errors["WaitForSingleObject(0)"] = err.Error()
	} else {
		p.Wait = &status
	}
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		p.Errors["GetExitCodeProcess"] = err.Error()
	} else {
		p.ExitCode = &code
	}
	return p
}

func (o *task43GraceObservation) sample(phase string) {
	r := task43GraceRecord{Phase: phase, OffsetNS: time.Since(o.start).Nanoseconds()}
	if o.owned.mu.TryLock() {
		r.Owned = task43GraceOwnedLocked(o.owned)
		o.owned.mu.Unlock()
	}
	r.Root = task43GraceInspectProcess(o.root, o.job, 0)
	var members task43GraceMembers
	err := queryWindowsJob(o.job, windows.JobObjectBasicProcessIdList, unsafe.Pointer(&members), uint32(unsafe.Sizeof(members)))
	r.Assigned, r.Listed, r.ListError = members.Assigned, members.Count, task43GraceError(err)
	r.Incomplete = err != nil || members.Count != members.Assigned || members.Count > uint32(len(members.IDs)) || members.Assigned > uint32(len(members.IDs))
	// On an API error even the returned slots are unknown. Never promote a
	// partial buffer or a later reopened PID to membership at the decision.
	if err == nil {
		count := min(members.Count, uint32(len(members.IDs)))
		for _, id := range members.IDs[:count] {
			if id == 0 || uint64(id) > uint64(^uint32(0)) {
				r.Members = append(r.Members, task43GraceProcess{ListedPID: id, Errors: map[string]string{"PID": "invalid listed PID; not opened"}})
				continue
			}
			handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(id))
			if err != nil {
				r.Members = append(r.Members, task43GraceProcess{ListedPID: id, Errors: map[string]string{"OpenProcess": err.Error()}})
				continue
			}
			p := task43GraceInspectProcess(handle, o.job, id)
			if err := windows.CloseHandle(handle); err != nil {
				p.Errors["CloseHandle"] = err.Error()
			}
			r.Members = append(r.Members, p)
		}
	}
	r.EndOffsetNS = time.Since(o.start).Nanoseconds()
	o.records = append(o.records, r)
}

func (o *task43GraceObservation) sampleLoop() {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	defer close(o.done)
	defer o.closeHandles()
	for {
		select {
		case request := <-o.requests:
			o.sample(request.phase)
			close(request.ack)
			if request.stop {
				return
			}
		case <-ticker.C:
			if o.periodicSamples < task43GraceSampleLimit {
				o.sample("sampling")
				o.periodicSamples++
			} else {
				o.periodicLimitHit = true
				ticker.Stop()
			}
		}
	}
}

func (o *task43GraceObservation) phase(phase string) {
	if o == nil {
		return
	}
	request := task43GraceRequest{phase: phase, ack: make(chan struct{})}
	select {
	case <-o.done:
		return
	case o.requests <- request:
		<-request.ack
	}
}

func (o *task43GraceObservation) closeHandles() {
	for _, h := range []*windows.Handle{&o.root, &o.job} {
		if *h != 0 {
			if err := windows.CloseHandle(*h); err != nil {
				o.closeErrors = append(o.closeErrors, fmt.Sprintf("CloseHandle(%d): %v", *h, err))
			}
			*h = 0
		}
	}
}

func (o *task43GraceObservation) finish(phase string) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		request := task43GraceRequest{phase: phase, stop: true, ack: make(chan struct{})}
		select {
		case <-o.done:
		case o.requests <- request:
			<-o.done
		}
		o.log(struct {
			Instrumented      string
			ObservationLimits string
			OriginalJob       windows.Handle
			OriginalRoot      windows.Handle
			DuplicateJob      windows.Handle
			DuplicateRoot     windows.Handle
			Initial           task43GraceOwned
			CaptureError      string
			PeriodicLimitHit  bool
			CloseErrors       []string
		}{
			Instrumented:      "test-only retained duplicate Job/root handles; lifetime/timing perturbing; delays last-handle KILL_ON_JOB_CLOSE fallback; not release qualification",
			ObservationLimits: "handle values are process-local and may be reused after close; offsets are monotonic nanoseconds since observer start at readiness; samples are non-atomic/racy, PID reuse possible; null means unknown; wait/code are independent API observations (259 alone does not prove alive); exact forced-decision capture may be unavailable; at most 32 listed members and 256 periodic samples",
			OriginalJob:       o.originalJob, OriginalRoot: o.originalRoot, Initial: o.initial,
			DuplicateJob: o.duplicateJob, DuplicateRoot: o.duplicateRoot,
			CaptureError: o.captureError, PeriodicLimitHit: o.periodicLimitHit, CloseErrors: o.closeErrors,
		})
		for _, record := range o.records {
			o.log(record)
		}
	})
}

func (o *task43GraceObservation) log(value interface{}) {
	encoded, err := json.Marshal(value)
	if err != nil {
		o.t.Logf("GRACE-OBSERVATION marshal error: %v", err)
		return
	}
	o.t.Logf("GRACE-OBSERVATION %s", encoded)
}
