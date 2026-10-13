//go:build linux

package session

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// The upstream failure scenario is recorded in creack/pty#216: GC stack
// movement can make an old ioctl pointer select a different terminal's slave.
func TestPTYAllocationMatchesKernelUnderStackShrink(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	var stop atomic.Bool
	gcDone := make(chan struct{})
	go func() {
		defer close(gcDone)
		for !stop.Load() {
			runtime.GC()
		}
	}()
	failures := make(chan error, 8)
	deadline := time.Now().Add(3 * time.Second)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for !stop.Load() && time.Now().Before(deadline) {
				shrinkPTYStack(1500)
				p, s, err := pty.Open()
				if err != nil {
					failures <- err
					stop.Store(true)
					return
				}
				index, err := unix.IoctlGetInt(int(p.Fd()), unix.TIOCGPTN)
				var stat unix.Stat_t
				statErr := unix.Fstat(int(s.Fd()), &stat)
				name := s.Name()
				_ = s.Close()
				_ = p.Close()
				if err != nil || statErr != nil || name != fmt.Sprintf("/dev/pts/%d", index) || unix.Minor(uint64(stat.Rdev)) != uint32(index) {
					failures <- fmt.Errorf("master index %d selected slave %s device %d; ioctl=%v stat=%v", index, name, unix.Minor(uint64(stat.Rdev)), err, statErr)
					stop.Store(true)
					return
				}
			}
		}()
	}
	workers.Wait()
	stop.Store(true)
	<-gcDone
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

//go:noinline
func shrinkPTYStack(depth int) byte {
	var pad [16]byte
	pad[depth&15] = byte(depth)
	if depth > 0 {
		pad[0] += shrinkPTYStack(depth - 1)
	}
	return pad[depth&15]
}
