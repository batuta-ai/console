package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/publication"
	"golang.org/x/sys/windows"
)

func TestTask43WindowsScriptJobDrainObservation(t *testing.T) {
	for attempt := 0; attempt < 3; attempt++ {
		t.Run(string(rune('A'+attempt)), func(t *testing.T) {
			home := testfixture.PrivateHome(t)
			path := filepath.Join(home, "actual shell ownership diagnostic.exe")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			quote := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "'"
			body := []byte("#!/bin/sh\n" + quote + " -test.run=^TestTask43WindowsNativeCommandLifetimeFixture$ -- task43-command-lifetime hold\n")
			if err := testfixture.WriteScript(t, path, body, 0700); err != nil {
				t.Fatal(err)
			}
			m := New(t.TempDir())
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			})
			env := testfixture.TerminalEnvironment(t, []string{"HOME=" + home, "PATH=" + home, "LANG=C.UTF-8"})
			runner := NativeRunner{Manager: m, Environment: env}
			output := &task43LifetimeOutput{ready: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := runner.Run(ctx, publication.Command{Executable: path, Directory: home, Observer: output})
				done <- err
			}()
			deadline, stop := context.WithTimeout(context.Background(), 6*time.Second)
			defer stop()
			select {
			case <-output.ready:
			case err := <-done:
				t.Fatal("diagnostic child exited before birth", err, output.output())
			case <-deadline.Done():
				t.Fatal("diagnostic child timed out", output.output())
			}
			child := task43ObserveLifetimeChild(t, output.output())
			m.mu.Lock()
			var owned *ownedProcess
			for candidate := range m.probes {
				if owned != nil {
					m.mu.Unlock()
					t.Fatal("unexpected simultaneous diagnostic admissions")
				}
				owned = candidate
			}
			m.mu.Unlock()
			if owned == nil {
				t.Fatal("actual original Job not found")
			}
			owned.mu.Lock()
			var job windows.Handle
			const jobObjectQuery = 0x0004
			err = windows.DuplicateHandle(windows.CurrentProcess(), owned.job, windows.CurrentProcess(), &job, jobObjectQuery, false, 0)
			owned.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(job)
			membership := func() int32 {
				var member int32
				ok, _, err := windowsProcessInJob.Call(uintptr(child), uintptr(job), uintptr(unsafe.Pointer(&member)))
				if ok == 0 {
					t.Fatal(err)
				}
				return member
			}
			accounting := func() windowsJobAccounting {
				var data windowsJobAccounting
				if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&data)), uint32(unsafe.Sizeof(data)), nil); err != nil {
					t.Fatal(err)
				}
				return data
			}
			t.Logf("before cancellation: childJobMember=%d accounting=%+v", membership(), accounting())
			var members struct {
				Assigned uint32
				Count    uint32
				IDs      [64]uintptr
			}
			if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&members)), uint32(unsafe.Sizeof(members)), nil); err != nil || members.Count != members.Assigned || members.Count > uint32(len(members.IDs)) {
				t.Fatal("actual job member enumeration", members, err)
			}
			var processes []windows.Handle
			var images []string
			for _, id := range members.IDs[:members.Count] {
				handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(id))
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(handle)
				var member int32
				ok, _, callErr := windowsProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&member)))
				if ok == 0 || member != 1 {
					t.Fatal("observed process not a member of original private job", id, member, callErr)
				}
				var created, exited, kernel, user windows.Filetime
				if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
					t.Fatal(err)
				}
				name := make([]uint16, 4096)
				size := uint32(len(name))
				if err := windows.QueryFullProcessImageName(handle, 0, &name[0], &size); err != nil {
					t.Fatal(err)
				}
				image := windows.UTF16ToString(name[:size])
				t.Logf("actual member id=%d birth=%d image=%s", id, uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), image)
				processes = append(processes, handle)
				images = append(images, image)
			}
			cancel()
			select {
			case err := <-done:
				t.Logf("Run completed: err=%v", err)
			case <-deadline.Done():
				t.Fatal("diagnostic command did not join", deadline.Err())
			}
			immediate, err := windows.WaitForSingleObject(child, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("at Run completion: childJobMember=%d accounting=%+v childWait=%d", membership(), accounting(), immediate)
			statuses := make([]uint32, len(processes))
			for i, handle := range processes {
				status, err := windows.WaitForSingleObject(handle, 0)
				if err != nil {
					t.Fatal(err)
				}
				statuses[i] = status
				t.Logf("at Run completion: actual member image=%s wait=%d", images[i], status)
			}
			started := time.Now()
			eventual, err := windows.WaitForSingleObject(child, 2000)
			t.Logf("bounded later observation: childWait=%d err=%v seconds=%f", eventual, err, time.Since(started).Seconds())
			if immediate != windows.WAIT_OBJECT_0 {
				t.Fatal("actual original held descendant not signaled at Run completion", immediate)
			}
			for i, handle := range processes {
				started := time.Now()
				eventual, err := windows.WaitForSingleObject(handle, 2000)
				t.Logf("bounded member observation: image=%s wait=%d err=%v seconds=%f", images[i], eventual, err, time.Since(started).Seconds())
				if statuses[i] != windows.WAIT_OBJECT_0 {
					t.Error("original Job member not signaled at Run completion", images[i], statuses[i])
				}
			}
		})
	}
}
