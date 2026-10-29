package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/integrations/batuta"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/testfixture"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func questionFixture(t *testing.T, svc *Service, root, workspace, mission, delivery string) (*journal.Store, map[string]any) {
	t.Helper()
	physical, err := testfixture.ResolvePath(root)
	if err != nil {
		t.Fatal(err)
	}
	path := serviceBindingFixture(t, root, delivery, delivery)
	if err := svc.Store.PutWorkspace(domain.Workspace{ID: workspace, Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.PutMission(domain.Mission{ID: mission, WorkspaceID: workspace, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	m, err := svc.BindMission(mission, MissionBindingRequest{WorkspaceID: workspace, PlanPath: path, DeliveryID: delivery})
	if err != nil {
		t.Fatal(err)
	}
	js, err := journal.Open(physical)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := json.Marshal(routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskWaitingInput, Attempts: []routing.GraphTaskAttempt{{Execution: 1, State: routing.GraphTaskWaitingInput, ChildRunID: "run", WorktreeID: "worktree", WorktreeRoot: physical, BaseHeadSHA: strings.Repeat("b", 40), Runtime: routing.RuntimeValue{Provider: "codex", Model: "fixture", Reasoning: "medium"}, Question: &routing.TaskQuestion{RequestID: "sha256:" + strings.Repeat("a", 64), Prompt: "Choose " + mission, ContextDigest: "sha256:" + strings.Repeat("c", 64)}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Append(delivery, journal.Record{Kind: loop.KindQuestion, TaskID: "task_1", Graph: graph, Detail: []byte(`{"execution":1}`)}); err != nil {
		t.Fatal(err)
	}
	return js, map[string]any{"mission_id": mission, "workspace_id": workspace, "workspace": workspace, "delivery": delivery, "task": "task_1", "execution": 1, "question_id": "sha256:" + strings.Repeat("a", 64), "opened_digest": m.Binding.OpenedDigest, "text": "answer"}
}
func questionRequest(t *testing.T, values map[string]any) batuta.AnswerRequest {
	t.Helper()
	b, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	var req batuta.AnswerRequest
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatal(err)
	}
	return req
}
func TestQuestionMissingIdentityNeverFallsBack(t *testing.T) {
	for _, field := range []string{"mission_id", "workspace_id", "delivery", "task", "execution", "question_id", "opened_digest"} {
		t.Run(field, func(t *testing.T) {
			svc := setupService(t)
			js, values := questionFixture(t, svc, t.TempDir(), "w", "m", "run")
			before, err := os.ReadFile(js.Path("run"))
			if err != nil {
				t.Fatal(err)
			}
			delete(values, field)
			id, err := svc.Answer(questionRequest(t, values))
			if err == nil || id != "" {
				t.Errorf("incomplete identity accepted: %s %v", id, err)
			}
			after, err := os.ReadFile(js.Path("run"))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Error("incomplete identity mutated native journal")
			}
		})
	}
}
func TestQuestionWatchExposesExactIdentity(t *testing.T) {
	svc := setupService(t)
	_, values := questionFixture(t, svc, t.TempDir(), "w", "m", "run")
	view, err := svc.WatchMission("m", "w", "run")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(view)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	pending, ok := raw["pending_question"].(map[string]any)
	if !ok {
		t.Fatalf("missing pending identity: %s", b)
	}
	target := pending["target"].(map[string]any)
	for _, key := range []string{"mission_id", "workspace_id", "delivery", "task", "question_id", "opened_digest"} {
		if target[key] != values[key] {
			t.Errorf("%s: %v", key, target)
		}
	}
	if target["execution"] != float64(1) || pending["prompt"] != "Choose m" {
		t.Fatalf("pending %v", pending)
	}
}

func TestQuestionForeignIdentityAndChangedArtifacts(t *testing.T) {
	for _, name := range []string{"mission", "workspace", "raw root", "delivery", "opening", "task", "question", "execution", "malformed question", "malformed opening", "unassigned", "missing catalog", "missing plan", "changed plan", "missing journal", "changed journal", "closed store"} {
		t.Run(name, func(t *testing.T) {
			svc := setupService(t)
			root := t.TempDir()
			js, values := questionFixture(t, svc, root, "w", "m", "run")
			other, foreign := questionFixture(t, svc, root, "w", "other", "other-run")
			third, _ := questionFixture(t, svc, t.TempDir(), "foreign", "third", "third-run")
			switch name {
			case "mission":
				values["mission_id"] = "other"
			case "workspace":
				values["workspace_id"] = "foreign"
			case "raw root":
				values["workspace"] = root
			case "delivery":
				values["delivery"] = foreign["delivery"]
			case "opening":
				values["opened_digest"] = foreign["opened_digest"]
			case "task":
				values["task"] = "task_2"
			case "question":
				values["question_id"] = "sha256:" + strings.Repeat("d", 64)
			case "execution":
				values["execution"] = 2
			case "malformed question":
				values["question_id"] = "bad"
			case "malformed opening":
				values["opened_digest"] = "bad"
			case "unassigned":
				if err := svc.Store.PutMission(domain.Mission{ID: "u", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
					t.Fatal(err)
				}
				values["mission_id"] = "u"
			case "missing catalog":
				values["mission_id"] = "absent"
			case "missing plan":
				if err := os.Remove(root + "/.batuta/plans/run.md"); err != nil {
					t.Fatal(err)
				}
			case "changed plan":
				if err := os.WriteFile(root+"/.batuta/plans/run.md", []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing journal":
				if err := os.Remove(js.Path("run")); err != nil {
					t.Fatal(err)
				}
			case "changed journal":
				if err := os.WriteFile(js.Path("run"), []byte("corrupt\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "closed store":
				if err := svc.Store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{js.Path("run"), other.Path("other-run"), third.Path("third-run")}
			before := make([][]byte, len(paths))
			for i, p := range paths {
				before[i], _ = os.ReadFile(p)
			}
			result, err := svc.AnswerQuestion(context.Background(), questionRequest(t, values))
			if err == nil || result.Effect != domain.AnswerRejected || result.Delivery != "" {
				t.Fatalf("accepted: %+v %v", result, err)
			}
			for i, p := range paths {
				after, _ := os.ReadFile(p)
				if string(after) != string(before[i]) {
					t.Errorf("mutated %s", p)
				}
			}
		})
	}
}

func TestQuestionContinuationTerminalAndDuplicate(t *testing.T) {
	for _, mode := range []string{"continuation", "terminal", "duplicate", "replacement", "not waiting"} {
		t.Run(mode, func(t *testing.T) {
			svc := setupService(t)
			js, values := questionFixture(t, svc, t.TempDir(), "w", "m", "run")
			req := questionRequest(t, values)
			records, err := js.Read("run")
			if err != nil {
				t.Fatal(err)
			}
			last := records[len(records)-1]
			switch mode {
			case "continuation":
				r, err := svc.AnswerQuestion(context.Background(), req)
				if err != nil || r.Effect != domain.AnswerRecorded {
					t.Fatalf("first answer: %+v %v", r, err)
				}
				current, err := js.Read("run")
				if err != nil {
					t.Fatal(err)
				}
				last = current[len(current)-1]
				var graph routing.DeliveryGraph
				if err := json.Unmarshal(last.Graph, &graph); err != nil {
					t.Fatal(err)
				}
				task := graph.Tasks[0]
				next := task.Attempts[len(task.Attempts)-1]
				q := *task.Attempts[0].Question
				q.Answer = nil
				if _, err := graph.RecordQuestion(req.Task, next.Execution, next.ChildRunID, q, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				last.Kind = loop.KindQuestion
				last.Graph, _ = json.Marshal(graph)
				last.Detail = []byte(`{"execution":2}`)

			case "terminal":
				last.Kind = loop.KindTerminal
				last.Detail = []byte(`{"state":"done"}`)
			case "not waiting":
				var graph routing.DeliveryGraph
				if err := json.Unmarshal(last.Graph, &graph); err != nil {
					t.Fatal(err)
				}
				graph.Tasks[0].State = routing.GraphTaskRunning
				last.Graph, _ = json.Marshal(graph)
			case "replacement":
				if err := os.Remove(js.Path("run")); err != nil {
					t.Fatal(err)
				}
				last = records[0]
				last.Detail = append([]byte(" "), last.Detail...)
			case "duplicate":
				result, err := svc.AnswerQuestion(context.Background(), req)
				if err != nil || result.Effect != domain.AnswerRecorded || result.Delivery != "run" {
					t.Fatalf("first answer: %+v %v", result, err)
				}
			}
			if mode != "duplicate" {
				last.Digest = ""
				if _, err := js.Append("run", last); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(js.Path("run"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := svc.AnswerQuestion(context.Background(), req)
			if err == nil || result.Effect != domain.AnswerRejected {
				t.Fatalf("old target accepted: %+v %v", result, err)
			}
			after, _ := os.ReadFile(js.Path("run"))
			if string(after) != string(before) {
				t.Fatal("rejection appended answer")
			}
			view, err := svc.WatchMission("m", "w", "run")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "continuation" {
				if view.PendingQuestion == nil || view.PendingQuestion.Target.Execution != 2 || view.PendingQuestion.Target.QuestionID != req.QuestionID {
					t.Fatalf("continuation %+v", view.PendingQuestion)
				}
				r, err := svc.AnswerQuestion(context.Background(), view.PendingQuestion.Target.Answer("next"))
				if err != nil || r.Effect != domain.AnswerRecorded {
					t.Fatalf("latest target %+v %v", r, err)
				}
			} else if view.PendingQuestion != nil {
				t.Fatalf("fabricated pending identity %+v", view.PendingQuestion)
			}
		})
	}
}

func TestQuestionConcurrentAndCanceled(t *testing.T) {
	svc := setupService(t)
	js, values := questionFixture(t, svc, t.TempDir(), "w", "m", "run")
	req := questionRequest(t, values)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := svc.AnswerQuestion(ctx, req)
	if !errors.Is(err, context.Canceled) || r.Effect != domain.AnswerRejected {
		t.Fatalf("canceled %+v %v", r, err)
	}
	type outcome struct {
		r   domain.AnswerResult
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() { <-start; r, err := svc.AnswerQuestion(context.Background(), req); results <- outcome{r, err} }()
	}
	close(start)
	accepted := 0
	for range 2 {
		got := <-results
		if got.err == nil {
			accepted++
		} else if !errors.Is(got.err, loop.ErrStaleQuestion) && !errors.Is(got.err, loop.ErrDeliveryOwned) {
			t.Fatalf("unexpected concurrent error: %+v %v", got.r, got.err)
		}
	}
	records, err := js.Read("run")
	if err != nil || accepted != 1 || len(records) != 3 || records[2].Kind != loop.KindAnswer {
		t.Fatalf("answers=%d records=%v error=%v", accepted, records, err)
	}
}

func checkQuestionMissionIsolationAndWorkspaceReplacement(t *testing.T, linkDirectory func(string, string) error) {
	svc := setupService(t)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := linkDirectory(root, alias); err != nil {
		t.Fatal(err)
	}
	_, firstValues := questionFixture(t, svc, alias, "w", "m", "first")
	_, secondValues := questionFixture(t, svc, alias, "w", "second", "second")
	foreignRoot := t.TempDir()
	third, thirdValues := questionFixture(t, svc, foreignRoot, "foreign", "third", "third")
	for _, values := range []map[string]any{firstValues, secondValues, thirdValues} {
		req := questionRequest(t, values)
		view, err := svc.WatchMission(req.MissionID, req.WorkspaceID, req.Delivery)
		if err != nil || view.PendingQuestion == nil || view.PendingQuestion.Target != req.Identity() || view.PendingQuestion.Prompt != "Choose "+req.MissionID {
			t.Fatalf("cross-mission projection %+v %v", view, err)
		}
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := linkDirectory(foreignRoot, alias); err != nil {
		t.Fatal(err)
	}
	view, err := svc.WatchMission("m", "w", "first")
	if err != nil || view.PendingQuestion != nil || view.Message != "workspace_changed" {
		t.Fatalf("replacement view %+v %v", view, err)
	}
	r, err := svc.AnswerQuestion(context.Background(), questionRequest(t, firstValues))
	if err == nil || r.Effect != domain.AnswerRejected {
		t.Fatalf("replacement answer %+v %v", r, err)
	}
	// The original journals remain at root after replacing the catalog alias.
	original, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		records, err := original.Read(id)
		if err != nil || len(records) != 2 {
			t.Fatalf("mutated %s: %v %v", id, records, err)
		}
	}
	records, err := third.Read("third")
	if err != nil || len(records) != 2 {
		t.Fatalf("mutated foreign journal %v %v", records, err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := linkDirectory(root, alias); err != nil {
		t.Fatal(err)
	}
	unchangedPaths := []string{original.Path("second"), third.Path("third")}
	before := make([][]byte, len(unchangedPaths))
	for i, path := range unchangedPaths {
		before[i], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	view, err = svc.WatchMission("m", "w", "first")
	if err != nil || view.PendingQuestion == nil || view.PendingQuestion.Target != questionRequest(t, firstValues).Identity() {
		t.Fatalf("current first question %+v %v", view, err)
	}
	r, err = svc.AnswerQuestion(context.Background(), view.PendingQuestion.Target.Answer("first answer"))
	if err != nil || r.Effect != domain.AnswerRecorded || r.Delivery != "first" || r.Target != view.PendingQuestion.Target {
		t.Fatalf("first answer %+v %v", r, err)
	}
	records, err = original.Read("first")
	if err != nil || len(records) != 3 || records[2].Kind != loop.KindAnswer {
		t.Fatalf("first native answer %v %v", records, err)
	}
	for i, path := range unchangedPaths {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before[i]) {
			t.Fatalf("first answer mutated %s", path)
		}
	}
	view, err = svc.WatchMission("second", "w", "second")
	if err != nil || view.PendingQuestion == nil || view.PendingQuestion.Target != questionRequest(t, secondValues).Identity() || view.PendingQuestion.Prompt != "Choose second" {
		t.Fatalf("current second question %+v %v", view, err)
	}
	r, err = svc.AnswerQuestion(context.Background(), view.PendingQuestion.Target.Answer("second answer"))
	if err != nil || r.Effect != domain.AnswerRecorded || r.Delivery != "second" || r.Target != view.PendingQuestion.Target {
		t.Fatalf("second answer %+v %v", r, err)
	}
	records, err = original.Read("second")
	if err != nil || len(records) != 3 || records[2].Kind != loop.KindAnswer {
		t.Fatalf("second native answer %v %v", records, err)
	}
}

func TestQuestionMissionIsolationAndWorkspaceReplacement(t *testing.T) {
	checkQuestionMissionIsolationAndWorkspaceReplacement(t, testfixture.DirectoryLink)
}
