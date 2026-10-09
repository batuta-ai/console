package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/accounts"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/review"
)

func task43CoreReviewPE() (bool, int) {
	if len(os.Args) < 8 || os.Args[2] != "--" || os.Args[3] != "task43-core-provider" {
		return false, 0
	}
	mode, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "native-fixture-mode"))
	if err != nil {
		return false, 0
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true, 90
	}
	if os.Args[5] != "--model" || os.Args[6] != "qa-native" {
		return true, 94
	}
	log := func(name string) bool {
		f, err := os.OpenFile(filepath.Join(os.Getenv("HOME"), name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return false
		}
		_, err = fmt.Fprintln(f, os.Args[4])
		return err == nil && f.Close() == nil
	}
	switch os.Args[4] {
	case "run":
		if !log("native-launches") {
			return true, 91
		}
		if err := os.WriteFile("out.txt", []byte("approved output\n"), 0600); err != nil {
			return true, 92
		}
		fmt.Println("completed")
	case "review":
		if !log("native-reviews") {
			return true, 91
		}
		if string(mode) == "invalid" {
			fmt.Println("TASK 1: DONE")
			return true, 0
		}
		fmt.Println("<<<FINDINGS")
		if string(mode) == "fix" {
			fmt.Println(`{"severity":"major","kind":"defect","file":"out.txt","line":1,"premise":"synthetic selected fixture defect","path":"out.txt:1 output path","verdict":"fix before ship"}`)
		}
		fmt.Println("FINDINGS>>>")
		fmt.Println("TASK 1: DONE")
	default:
		return true, 93
	}
	return true, 0
}

func TestTask43WindowsNativeCoreQualifiedReview(t *testing.T) {
	image := task43CoreImage(t)
	for _, test := range []struct {
		mode, outcome, state string
		code                 int
		cleared              bool
	}{{"valid", "SHIP", loop.StateDone, 0, true}, {"fix", "FIX_BEFORE_SHIP", loop.StateReviewBlocked, 2, false}, {"invalid", "REWORK", loop.StateReviewBlocked, 3, false}} {
		t.Run(test.mode, func(t *testing.T) {
			svc, a, skills, env, values := task43CoreApprovedFixture(t)
			svc.NativeExecutorExecutable = image
			home := accounts.ExplicitHome(values["codex"].Environment)
			task43CoreWriteFile(t, filepath.Join(home, "native-fixture-mode"), test.mode)
			state, err, active := task43CoreParentRun(t, svc, a, skills, env, values, "")
			if err != nil || !active || state != test.state {
				t.Fatal("actual Core reviewed lifecycle", state, active, err)
			}
			opts := loop.SupervisionOptions{Workspace: a.Identity.Binding.CanonicalRoot, Delivery: batuta.ExecutionDelivery(a)}
			observation, err := loop.ObserveSupervision(opts)
			if err != nil || observation.Review == nil {
				t.Fatal("actual review job missing", observation, err)
			}
			job := observation.Review
			if job.State != "reported" || job.Outcome != test.outcome || job.ExitCode != test.code || job.Attempts != 1 || job.ApprovalReceiptID != a.ID || job.PlanContentDigest != a.Identity.Version {
				t.Fatal("actual review verdict/receipt mapping", job, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			gate, err := loop.CheckSupervisionGate(ctx, opts)
			if err != nil || !gate.Required || gate.Cleared != test.cleared {
				t.Fatal("actual review gate", gate, err)
			}
			for _, name := range []string{"manifest.json", "findings.json", "review.md", "state.json"} {
				data, err := os.ReadFile(filepath.Join(job.Artifacts, name))
				if err != nil || len(data) == 0 || fmt.Sprintf("%x", sha256.Sum256(data)) != job.ArtifactDigests[name] {
					t.Fatal("actual review artifact digest", name, err)
				}
			}
			data, err := os.ReadFile(filepath.Join(job.Artifacts, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint review.IncrementalState
			if err := json.Unmarshal(data, &checkpoint); err != nil {
				t.Fatal(err)
			}
			if test.mode == "invalid" {
				if checkpoint.Head != job.Base || len(checkpoint.Pending) == 0 {
					t.Fatal("invalid review advanced covered checkpoint", checkpoint)
				}
			} else if checkpoint.Head != job.FinalCommit || len(checkpoint.Pending) != 0 {
				t.Fatal("covered review checkpoint missing", checkpoint)
			}
			reviews, err := os.ReadFile(filepath.Join(home, "native-reviews"))
			if err != nil || !strings.Contains(string(reviews), "review\n") {
				t.Fatal("review escaped selected local PE/HOME", string(reviews), err)
			}
			view, err := svc.ExecutionState(ctx, "m", "w", a.ID)
			if err != nil || view.Watch == nil || view.Watch.Header["state"] != test.state {
				t.Fatal("actual parent watch disagrees with Core", view, err)
			}
			if test.cleared {
				path := filepath.Join(job.Artifacts, "review.md")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\ntampered\n")...), 0600); err != nil {
					t.Fatal(err)
				}
				gate, err := loop.CheckSupervisionGate(ctx, opts)
				if err == nil && gate.Cleared {
					t.Fatal("changed artifact retained completed review gate", gate)
				}
			}
		})
	}
}
