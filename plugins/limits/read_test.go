package limits

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
)

func TestImportSizeBound(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, adapter := range []string{"codex", "claude"} {
		for _, size := range []int{16*1024 - 1, 16 * 1024, 16*1024 + 1} {
			t.Run(fmt.Sprintf("%s/%d", adapter, size), func(t *testing.T) {
				home := t.TempDir()
				a := domain.Account{ID: "size", Adapter: adapter}
				fields := importFields(now.Add(-time.Minute), 41)
				fields["origin"] = ""
				fields["origin"] = strings.Repeat("x", size-len(encodeImport(t, fields)))
				raw := encodeImport(t, fields)
				if len(raw) != size || !json.Valid(raw) {
					t.Fatal("fixture is not otherwise-valid JSON of the requested size")
				}
				path := putImport(t, home, a, importJSON(t, now.Add(-time.Minute), 62))
				p := New(&timedDisk{disk: Disk{Home: home}, now: now})
				if s := p.Snapshot(a); s.Percent == nil || *s.Percent != 62 {
					t.Fatalf("initial measurement: %+v", s)
				}
				putImport(t, home, a, raw)
				s := p.Snapshot(a)
				if size > 16*1024 {
					requireEmpty(t, s, domain.SourceUnknown)
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					requireEmpty(t, p.Snapshot(a), domain.SourceUnavailable)
					return
				}
				if s.Source != domain.SourceCurrent || s.Percent == nil || *s.Percent != 41 || s.ObservedAt == nil || !s.ObservedAt.Equal(now.Add(-time.Minute)) {
					t.Fatalf("valid bounded import: %+v", s)
				}
			})
		}
	}
}

// The watchdog is test containment only: a regressed blocking importer is killed
// and reaped, so the test itself leaves no blocked goroutines, processes or FDs.
// Production must return normally without a writer or a timeout.
func TestImportNonregularFiles(t *testing.T) {
	if home := os.Getenv("CONSOLE_LIMITS_PIPE_TEST_HOME"); home != "" {
		testImportFileKind(t, home, os.Getenv("CONSOLE_LIMITS_PIPE_TEST_ADAPTER"), os.Getenv("CONSOLE_LIMITS_PIPE_TEST_KIND"))
		return
	}
	for _, adapter := range []string{"codex", "claude"} {
		for _, kind := range []string{"fifo", "fifo-symlink", "directory", "directory-symlink", "regular-symlink"} {
			t.Run(adapter+"/"+kind, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestImportNonregularFiles$", "-test.v")
				cmd.Env = append(os.Environ(), "CONSOLE_LIMITS_PIPE_TEST_HOME="+t.TempDir(), "CONSOLE_LIMITS_PIPE_TEST_ADAPTER="+adapter, "CONSOLE_LIMITS_PIPE_TEST_KIND="+kind)
				out, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("importer blocked on %s without a writer; child killed and reaped: %s", kind, out)
				}
				if err != nil {
					t.Fatalf("file-kind regression: %v\n%s", err, out)
				}
			})
		}
	}
}

func testImportFileKind(t *testing.T, home, adapter, kind string) {
	t.Helper()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{"Snapshot", "All"} {
		t.Run(mode, func(t *testing.T) {
			a := domain.Account{ID: "file-kind-" + mode, Adapter: adapter}
			path := putImport(t, home, a, importJSON(t, now.Add(-time.Minute), 41))
			p := New(&timedDisk{disk: Disk{Home: home}, now: now})
			collect := func() domain.LimitSnapshot {
				if mode == "All" {
					now = now.Add(15 * time.Second)
					return p.allAtTime([]domain.Account{a}, now)[0]
				}
				return p.Snapshot(a)
			}
			if s := collect(); s.Percent == nil || *s.Percent != 41 {
				t.Fatalf("initial measurement: %+v", s)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := path
			if strings.HasSuffix(kind, "-symlink") {
				target = filepath.Join(t.TempDir(), "target")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "fifo", "fifo-symlink":
				if err := syscall.Mkfifo(target, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory", "directory-symlink":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "regular-symlink":
				if err := os.WriteFile(target, importJSON(t, now.Add(-time.Minute), 62), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := openDescriptorCount(t)
			for range 32 {
				s := collect()
				if kind == "regular-symlink" {
					if s.Percent == nil || *s.Percent != 62 || !strings.HasPrefix(s.Origin, "imported:"+adapter) {
						t.Fatalf("regular symlink lost measurement: %+v", s)
					}
				} else {
					requireEmpty(t, s, domain.SourceUnknown)
					if s.Origin != "imported:"+adapter {
						t.Fatalf("invalid import lost provenance: %+v", s)
					}
				}
			}
			if after := openDescriptorCount(t); after != before {
				t.Fatalf("descriptor leak after repeated imports: before=%d after=%d", before, after)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if kind != "regular-symlink" {
				requireEmpty(t, collect(), domain.SourceUnavailable)
			}
		})
	}
}

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	path := "/dev/fd"
	if runtime.GOOS == "linux" {
		path = "/proc/self/fd"
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// gatedDisk delays delivery of one real observation. Channels establish the
// exact collection order; measurements come from real files at a fixed time.
type gatedDisk struct {
	disk    Disk
	account string
	entered chan struct{}
	release chan struct{}
	blocked atomic.Bool
}

var readTestNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func (d *gatedDisk) Snapshot(a domain.Account) domain.LimitSnapshot {
	s := d.disk.snapshotAt(a, readTestNow)
	if a.ID == d.account && d.blocked.CompareAndSwap(false, true) {
		close(d.entered)
		<-d.release
	}
	return s
}

// Assert the concrete regression directly before calling another collector.
// TryLock cannot block, so the old global-lock implementation fails cleanly.
func requireCollectorOutsideGlobalLock(t *testing.T, p *Plugin) {
	t.Helper()
	if !p.mu.TryLock() {
		t.Fatal("plugin global mutex is held during collector I/O")
	}
	p.mu.Unlock()
}

func TestCollectionDoesNotLockUnrelatedAccounts(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		for _, mode := range []string{"Snapshot", "All"} {
			t.Run(adapter+"/"+mode, func(t *testing.T) {
				home := t.TempDir()
				slow := domain.Account{ID: "slow", Adapter: adapter}
				fast := domain.Account{ID: "fast", Adapter: adapter, Label: "Unrelated"}
				putImport(t, home, slow, importJSON(t, readTestNow.Add(-time.Minute), 41))
				putImport(t, home, fast, importJSON(t, readTestNow.Add(-time.Minute), 62))
				d := &gatedDisk{disk: Disk{Home: home}, account: slow.ID, entered: make(chan struct{}), release: make(chan struct{})}
				p := New(d)
				collect := func(a domain.Account) domain.LimitSnapshot {
					if mode == "All" {
						return p.All([]domain.Account{a})[0]
					}
					return p.Snapshot(a)
				}
				done := make(chan struct{})
				go func() { defer close(done); collect(slow) }()
				<-d.entered
				defer func() { close(d.release); <-done }()
				requireCollectorOutsideGlobalLock(t, p)
				s := collect(fast)
				if s.Source != domain.SourceCurrent || s.Percent == nil || *s.Percent != 62 || s.Label != fast.Label {
					t.Fatalf("unrelated account: %+v", s)
				}
			})
		}
	}
}

func TestDelayedBatchCannotRestoreInvalidatedObservation(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			home := t.TempDir()
			a := domain.Account{ID: "first", Adapter: adapter}
			b := domain.Account{ID: "last", Adapter: adapter}
			putImport(t, home, a, importJSON(t, readTestNow.Add(-time.Minute), 41))
			putImport(t, home, b, importJSON(t, readTestNow.Add(-time.Minute), 62))
			d := &gatedDisk{disk: Disk{Home: home}, account: b.ID, entered: make(chan struct{}), release: make(chan struct{})}
			p := New(d)
			done := make(chan struct{})
			go func() { defer close(done); p.All([]domain.Account{a, b}) }()
			<-d.entered // a was collected; b is still in flight.
			release := sync.OnceFunc(func() { close(d.release); <-done })
			defer release()
			requireCollectorOutsideGlobalLock(t, p)
			putImport(t, home, a, []byte(`{"percent":null}`))
			requireEmpty(t, p.Snapshot(a), domain.SourceUnknown)
			release()
			requireEmpty(t, p.All([]domain.Account{a, b})[0], domain.SourceUnknown)
		})
	}
}

func TestLateAccountCollectionCannotRestoreInvalidatedObservation(t *testing.T) {
	for _, adapter := range []string{"codex", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			home := t.TempDir()
			a := domain.Account{ID: "ordered", Adapter: adapter}
			path := putImport(t, home, a, importJSON(t, readTestNow.Add(-time.Minute), 41))
			d := &gatedDisk{disk: Disk{Home: home}, account: a.ID, entered: make(chan struct{}), release: make(chan struct{})}
			p := New(d)
			firstDone := make(chan domain.LimitSnapshot, 1)
			go func() { firstDone <- p.Snapshot(a) }()
			<-d.entered
			release := sync.OnceFunc(func() {
				close(d.release)
				if first := <-firstDone; first.Percent == nil || *first.Percent != 41 {
					t.Errorf("first attempt lost its own observation: %+v", first)
				}
			})
			defer release()
			requireCollectorOutsideGlobalLock(t, p)
			putImport(t, home, a, []byte(`{"percent":null}`))
			requireEmpty(t, p.Snapshot(a), domain.SourceUnknown)
			release()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			requireEmpty(t, p.Snapshot(a), domain.SourceUnavailable)
		})
	}
}
