package batuta

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func TestQuestionProjectionAndExactWriter(t *testing.T) {
	for _, state := range []string{"waiting", "done", "running", "attempt running", "invalid task", "unassigned", "changed opening", "changed plan"} {
		t.Run(state, func(t *testing.T) {
			root, err := CanonicalWorkspace(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := routing.PlanPath("alpha")
			body := []byte("# Plan — alpha\n**Status:** approved\n- [ ] 1. Work — data/high\n  Accept: verified\n")
			if err := os.MkdirAll(filepath.Join(root, ".batuta/plans"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
				t.Fatal(err)
			}
			plan, err := ExactPlan(root, path)
			if err != nil {
				t.Fatal(err)
			}
			js, err := journal.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			qid := "sha256:" + strings.Repeat("a", 64)
			attempt := routing.GraphTaskAttempt{Execution: 1, State: routing.GraphTaskWaitingInput, ChildRunID: "run", WorktreeID: "wt", WorktreeRoot: root, BaseHeadSHA: strings.Repeat("b", 40), Runtime: routing.RuntimeValue{Provider: "codex", Model: "fixture", Reasoning: "medium"}, Question: &routing.TaskQuestion{RequestID: qid, Prompt: "Exact prompt", ContextDigest: "sha256:" + strings.Repeat("c", 64)}}
			graph := routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskWaitingInput, Attempts: []routing.GraphTaskAttempt{attempt}}}}
			if state == "invalid task" {
				graph.Tasks[0].TaskID = " invalid "
			}
			encoded, _ := json.Marshal(graph)
			detail, _ := json.Marshal(map[string]string{"workspace": root, "slug": "alpha", "plan_path": path, "plan_digest": plan.Set.Digest})
			opened, err := js.Append("run", journal.Record{Kind: loop.KindOpened, Graph: encoded, Detail: detail})
			if err != nil {
				t.Fatal(err)
			}
			binding := domain.MissionBinding{WorkspaceID: "w", WorkspaceRoot: root, CanonicalRoot: root, PlanPath: path, PlanSlug: "alpha", PlanDigest: plan.Set.Digest, DeliveryID: "run", OpenedDigest: opened.Digest}
			if state == "done" {
				if _, err := js.Append("run", journal.Record{Kind: loop.KindTerminal, Graph: encoded, Detail: []byte(`{"state":"done"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "running" || state == "attempt running" {
				if state == "running" {
					graph.Tasks[0].State = routing.GraphTaskRunning
				} else {
					graph.Tasks[0].Attempts[0].State = routing.GraphTaskRunning
				}
				encoded, _ = json.Marshal(graph)
				if _, err := js.Append("run", journal.Record{Kind: loop.KindStarted, Graph: encoded, Detail: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "unassigned" {
				binding.DeliveryID = ""
				binding.OpenedDigest = ""
			}
			if state == "changed opening" {
				binding.OpenedDigest = "sha256:" + strings.Repeat("d", 64)
			}
			if state == "changed plan" {
				binding.PlanDigest = "sha256:" + strings.Repeat("d", 64)
			}
			view, err := ProjectMission(root, binding)
			if state == "changed opening" || state == "changed plan" {
				if !errors.Is(err, ErrArtifactInvalid) {
					t.Fatalf("changed binding %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if state != "waiting" {
				if view.PendingQuestion != nil {
					t.Fatalf("fabricated identity %+v", view.PendingQuestion)
				}
				return
			}
			pending := view.PendingQuestion
			if pending == nil || pending.Prompt != "Exact prompt" || pending.Target.OpenedDigest != opened.Digest || view.Detail["question"] != pending.Prompt {
				t.Fatalf("pending %+v", view)
			}
			pending.Target.MissionID = "m"
			pending.Target.WorkspaceID = "w"
			req := pending.Target.Answer("answer")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := Answer(ctx, root, req); !errors.Is(err, context.Canceled) {
				t.Fatalf("context lost %v", err)
			}
			id, err := Answer(context.Background(), root, req)
			if err != nil || id != "run" {
				t.Fatalf("native answer %s %v", id, err)
			}
			if _, err := Answer(context.Background(), root, req); !errors.Is(err, loop.ErrStaleQuestion) {
				t.Fatalf("duplicate %v", err)
			}
			records, err := js.Read("run")
			if err != nil || len(records) != 2 || records[1].Kind != loop.KindAnswer {
				t.Fatalf("native answer count %v %v", records, err)
			}
		})
	}
}
