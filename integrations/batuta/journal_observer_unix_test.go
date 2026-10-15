//go:build darwin || linux

package batuta

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/loop"
)

// A real Git process blocks reading HEAD. Opening its FIFO writer proves the
// post-capture reader is live; no transient stack or timing inference is needed.
func TestApprovedObserverLifetimeThroughGit(t *testing.T) {
	for _, mode := range []string{"cancel", "cause", "budget"} {
		t.Run(mode, func(t *testing.T) {
			root, b, a := supervisedProjectionFixture(t)
			appendProjectionTerminal(t, root, b)
			observed, err := loop.ObserveSupervision(loop.SupervisionOptions{Workspace: root, Delivery: b.DeliveryID})
			if err != nil || observed.Review == nil || observed.Review.ID == "" {
				t.Fatal("review identity", observed, err)
			}
			job := observed.Review
			snapshot := t.TempDir()
			cmd := exec.Command("git", "init", "--quiet", snapshot)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, output)
			}
			head := filepath.Join(snapshot, ".git", "HEAD")
			if err := os.Remove(head); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(head, 0600); err != nil {
				t.Fatal(err)
			}
			job.State, job.Snapshot = "reported", snapshot
			directory := filepath.Join(root, ".batuta", "reviews", "supervision", job.ID)
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "job.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			type result struct {
				view domain.WatchView
				err  error
			}
			done := make(chan result, 1)
			go func() {
				var view domain.WatchView
				var err error
				if mode == "budget" {
					view, err = ProjectApprovedMission(root, b, a)
				} else {
					view, err = ProjectApprovedMissionContext(ctx, root, b, a)
				}
				done <- result{view, err}
			}()
			type opened struct {
				file *os.File
				err  error
			}
			ready := make(chan opened, 1)
			go func() { f, err := os.OpenFile(head, os.O_WRONLY, 0); ready <- opened{f, err} }()
			var writer *os.File
			received := false
			joined := false
			defer func() {
				cancel(nil)
				if !received {
					// Release the FIFO opener even if Git failed before the handshake.
					unblock, err := os.OpenFile(head, os.O_RDWR, 0)
					if err != nil {
						t.Error("FIFO cleanup:", err)
						return
					}
					r := <-ready
					writer = r.file
					unblock.Close()
				}
				if writer != nil {
					writer.Close()
				}
				if !joined {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("Git observer cleanup did not join")
					}
				}
			}()
			select {
			case r := <-ready:
				received = true
				if r.err != nil {
					t.Fatal(r.err)
				}
				writer = r.file
			case result := <-done:
				joined = true
				t.Fatalf("observer never reached Git HEAD: %+v", result)
			case <-time.After(15 * time.Second):
				t.Fatal("Git did not open HEAD")
			}
			cause := context.Canceled
			if mode == "cause" {
				cause = errors.New("parent stopped post-capture Git")
			}
			if mode != "budget" {
				cancel(cause)
			} else {
				cause = ErrArtifactIO
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(7 * time.Second):
				t.Error("observer outlived its context while Git retained HEAD")
				writer.Close()
				got = <-done
			}
			joined = true
			if !errors.Is(got.err, cause) || got.view.Available || got.view.Source != domain.SourceUnavailable || got.view.Progression != nil {
				t.Errorf("post-capture observation=%+v err=%v; want %v", got.view, got.err, cause)
			}
			if mode == "budget" && errors.Is(got.err, context.DeadlineExceeded) {
				t.Error("local Git budget impersonated client deadline", got.err)
			}
		})
	}
}
