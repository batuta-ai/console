package session

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TEMPORARY NONSHIPPING / NEVER-SHIP diagnostic overlay. Remove this file and
// the single retire call before any merge to main or release. Caller holds
// p.mu. All observations are read-only, but APIs and output perturb timing;
// bounded calls/buffers do not guarantee a wall-clock bound on kernel/output I/O.
const graceDecisionOutputLimit = 2 * 1024 * 1024

const graceDecisionLimits = "TEMPORARY NONSHIPPING NEVER-SHIP; timing perturbation under p.mu before forced assignment; non-atomic observations in Steps order; offsets are monotonic nanoseconds from helper entry AFTER original accounting query (query start/end unknown); null means unknown; raw accounting/list buffers on errors are not valid results; at most 32 members, 1024 UTF-16 image units, no query retries; list/open/identity may race exit and PID reuse; later membership/birth cannot prove identity at accounting decision; wait and exit code are independent (259 alone does not prove alive); handle values may be reused after close; output may block/fail; total output failure cannot be made observable through failed streams"

type graceDecisionStep struct {
	API     string
	StartNS int64
	EndNS   int64
}

type graceDecisionProcess struct {
	ListedPID   uintptr
	Handle      windows.Handle
	PID         *uint32
	Birth       *uint64
	InJob       *bool
	Wait        *uint32
	ExitCode    *uint32
	Image       *string
	HandleFlags *uint32
	Errors      map[string]string
	CloseError  string
	Steps       []graceDecisionStep
}

type graceDecisionOwned struct {
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

type graceDecisionList struct {
	Assigned uint32
	Count    uint32
	IDs      [32]uintptr
}

type graceDecisionRecord struct {
	ObserverPID     int
	TraceStartUTC   string
	TraceID         uint64 // NEVER-SHIP correlation only; not process identity.
	Limits          string
	StartUTC        string
	EndNS           int64
	Job             windows.Handle
	Owned           graceDecisionOwned
	Accounting      windowsJobAccounting
	AccountingError string
	ListStep        graceDecisionStep
	RawList         graceDecisionList
	Listed          uint32
	Incomplete      bool
	ListError       string
	Root            graceDecisionProcess
	Members         []graceDecisionProcess
	OutputError     string
}

func graceDecisionError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 256 {
		return s[:256] + " [error text truncated at 256 bytes]"
	}
	return s
}

func (p *graceDecisionProcess) observe(start time.Time, api string, query func() error) {
	step := graceDecisionStep{API: api, StartNS: time.Since(start).Nanoseconds()}
	err := query()
	step.EndNS = time.Since(start).Nanoseconds()
	p.Steps = append(p.Steps, step)
	if err != nil {
		p.Errors[api] = graceDecisionError(err)
	}
}

func inspectGraceDecisionProcess(p graceDecisionProcess, job windows.Handle, start time.Time) graceDecisionProcess {
	p.observe(start, "GetProcessId", func() error {
		pid, err := windows.GetProcessId(p.Handle)
		if err == nil {
			p.PID = &pid
		}
		return err
	})
	p.observe(start, "GetProcessTimes", func() error {
		var created, exited, kernel, user windows.Filetime
		err := windows.GetProcessTimes(p.Handle, &created, &exited, &kernel, &user)
		if err == nil {
			birth := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
			p.Birth = &birth
		}
		return err
	})
	// NULL tests any Job, not the specific owned Job whose membership is unknown.
	if job == 0 {
		p.Errors["IsProcessInJob"] = "missing owned Job; membership unknown; API skipped (NULL tests any Job)"
	} else {
		p.observe(start, "IsProcessInJob", func() error {
			var inJob int32
			ok, _, err := windowsProcessInJob.Call(uintptr(p.Handle), uintptr(job), uintptr(unsafe.Pointer(&inJob)))
			if ok == 0 {
				return fmt.Errorf("IsProcessInJob returned FALSE: %w", err)
			}
			member := inJob != 0
			p.InJob = &member
			return nil
		})
	}
	p.observe(start, "WaitForSingleObject(0)", func() error {
		status, err := windows.WaitForSingleObject(p.Handle, 0)
		if err == nil {
			p.Wait = &status
		}
		return err
	})
	p.observe(start, "GetExitCodeProcess", func() error {
		var code uint32
		err := windows.GetExitCodeProcess(p.Handle, &code)
		if err == nil {
			p.ExitCode = &code
		}
		return err
	})
	p.observe(start, "QueryFullProcessImageName", func() error {
		var buffer [1024]uint16
		size := uint32(len(buffer))
		if err := windows.QueryFullProcessImageName(p.Handle, 0, &buffer[0], &size); err != nil {
			return fmt.Errorf("bounded image query (1024 UTF-16 units; insufficient buffer means truncated/unknown; no retry): %w", err)
		}
		if size >= uint32(len(buffer)) {
			return fmt.Errorf("image length %d reaches 1024 UTF-16 bound; full image unknown", size)
		}
		path := windows.UTF16ToString(buffer[:size])
		p.Image = &path
		return nil
	})
	p.observe(start, "GetHandleInformation", func() error {
		var flags uint32
		ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation").Call(uintptr(p.Handle), uintptr(unsafe.Pointer(&flags)))
		if ok == 0 {
			return fmt.Errorf("GetHandleInformation returned FALSE: %w", err)
		}
		p.HandleFlags = &flags
		return nil
	})
	return p
}

func collectGraceDecisionMember(id uintptr, job windows.Handle, start time.Time) graceDecisionProcess {
	p := graceDecisionProcess{ListedPID: id, Errors: make(map[string]string)}
	if id == 0 || uint64(id) > uint64(^uint32(0)) {
		p.Errors["PID"] = "invalid listed PID; not opened; identity unknown"
		return p
	}
	p.observe(start, "OpenProcess(query/synchronize, inherit=false)", func() error {
		var err error
		p.Handle, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(id))
		return err
	})
	if p.Handle == 0 {
		return p
	}
	p = inspectGraceDecisionProcess(p, job, start)
	p.observe(start, "CloseHandle", func() error {
		err := windows.CloseHandle(p.Handle)
		p.CloseError = graceDecisionError(err)
		return err
	})
	return p
}

func collectGraceDecisionDiagnostic(p *ownedProcess, accounting windowsJobAccounting, accountingErr error) graceDecisionRecord {
	start := time.Now()
	r := graceDecisionRecord{
		Limits: graceDecisionLimits, StartUTC: start.UTC().Format(time.RFC3339Nano), Job: p.job,
		Accounting: accounting, AccountingError: graceDecisionError(accountingErr),
		Owned: graceDecisionOwned{
			PID: p.pid, Native: p.native, Exited: p.exited, Retired: p.retired,
			Signaled: p.signaled, Forced: p.forced, Error: graceDecisionError(p.err),
			TerminationObserved: p.terminationObserved, TerminationTotalObserved: p.terminationTotalObserved,
			TerminationTotal: p.terminationTotal,
		},
	}
	// exited publishes exitCode; an unpublished exit code remains unknown.
	if p.exited {
		code := p.exitCode
		r.Owned.ExitCode = &code
	}
	r.ListStep = graceDecisionStep{API: "QueryInformationJobObject(BasicProcessIdList)", StartNS: time.Since(start).Nanoseconds()}
	var err error
	// NULL selects the caller's ambient Job, which is outside this trace's scope.
	if p.job == 0 {
		r.ListStep.API += " skipped: missing owned Job"
		err = fmt.Errorf("missing owned Job; member list unknown; API skipped (NULL selects caller's Job)")
	} else {
		err = queryWindowsJob(p.job, windows.JobObjectBasicProcessIdList, unsafe.Pointer(&r.RawList), uint32(unsafe.Sizeof(r.RawList)))
	}
	r.ListStep.EndNS = time.Since(start).Nanoseconds()
	r.Listed, r.ListError = r.RawList.Count, graceDecisionError(err)
	r.Incomplete = err != nil || r.RawList.Count != r.RawList.Assigned || r.RawList.Count > 32 || r.RawList.Assigned > 32
	r.Root = inspectGraceDecisionProcess(graceDecisionProcess{Handle: p.root, Errors: make(map[string]string)}, p.job, start)
	// Failed list buffers remain raw/unknown. Never open a PID from one, and
	// never interpret reopened handles as identity at the earlier decision.
	if err == nil {
		for _, id := range r.RawList.IDs[:min(r.RawList.Count, 32)] {
			r.Members = append(r.Members, collectGraceDecisionMember(id, p.job, start))
		}
	}
	r.EndNS = time.Since(start).Nanoseconds()
	return r
}

func writeGraceDecisionDiagnostic(stream *os.File, r graceDecisionRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(append([]byte("GRACE-DECISION "), data...), '\n')
	if len(data) > graceDecisionOutputLimit {
		return fmt.Errorf("encoded trace exceeds %d byte bound; record not written", graceDecisionOutputLimit)
	}
	n, err := stream.Write(data)
	if err != nil {
		return fmt.Errorf("trace output wrote %d/%d bytes: %w", n, len(data), err)
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func traceGraceDecisionDiagnostic(p *ownedProcess, accounting windowsJobAccounting, accountingErr error) {
	r := collectGraceDecisionDiagnostic(p, accounting, accountingErr)
	if d := p.decision.Load(); d != nil {
		r.TraceID = d.id
		r.ObserverPID = os.Getpid()
		r.TraceStartUTC = d.start.UTC().Format(time.RFC3339Nano)
	}
	if err := writeGraceDecisionDiagnostic(os.Stderr, r); err != nil {
		// Separate delivery-error event, not an observation retry. No output
		// error may enter owned state. Failed streams cannot guarantee evidence.
		delivery := graceDecisionRecord{TraceID: r.TraceID, ObserverPID: r.ObserverPID, TraceStartUTC: r.TraceStartUTC, Limits: graceDecisionLimits, StartUTC: r.StartUTC, OutputError: graceDecisionError(err)}
		if err := writeGraceDecisionDiagnostic(os.Stderr, delivery); err != nil {
			// Last bounded best-effort runtime stderr diagnostic retains both
			// errors; runtime printing exposes no further write result.
			data, marshalErr := json.Marshal(struct{ OutputError, DeliveryError string }{delivery.OutputError, graceDecisionError(err)})
			if marshalErr != nil {
				println("GRACE-DECISION {\"OutputError\":\"delivery-error JSON encoding failed; NEVER-SHIP\"}")
				return
			}
			println("GRACE-DECISION " + string(data))
		}
	}
}
