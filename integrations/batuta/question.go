package batuta

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

var questionDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidateAnswer(req AnswerRequest) error {
	if req.MissionID == "" || req.WorkspaceID == "" || strings.TrimSpace(req.Text) == "" || !validNativeQuestion(req.Identity()) {
		return loop.ErrInvalidQuestionTarget
	}
	return nil
}

func validNativeQuestion(q domain.QuestionIdentity) bool {
	return journal.ValidDeliveryID(q.Delivery) && q.Task != "" && len(q.Task) <= 256 &&
		strings.TrimSpace(q.Task) == q.Task && !strings.ContainsAny(q.Task, "\x00\r\n") &&
		q.Execution > 0 && q.Execution <= routing.MaxTaskExecutions &&
		questionDigest.MatchString(q.QuestionID) && questionDigest.MatchString(q.OpenedDigest)
}

// Answer uses only Core's exact writer. The returned ID must survive errors:
// Core can append successfully and then fail to release native ownership.
func Answer(ctx context.Context, workspace string, req AnswerRequest) (string, error) {
	if err := ValidateAnswer(req); err != nil {
		return "", err
	}
	return loop.AnswerQuestion(ctx, workspace, loop.QuestionTarget{DeliveryID: req.Delivery, TaskID: req.Task, Execution: req.Execution, QuestionID: req.QuestionID, OpenedDigest: req.OpenedDigest}, req.Text)
}

func pendingQuestion(records []journal.Record, view domain.WatchView) *domain.PendingQuestion {
	if len(records) == 0 || view.Header["state"] != "waiting_input" {
		return nil
	}
	var graph routing.DeliveryGraph
	if json.Unmarshal(records[len(records)-1].Graph, &graph) != nil {
		return nil
	}
	for _, task := range graph.Tasks {
		if task.TaskID != view.Detail["task"] || task.State != routing.GraphTaskWaitingInput || len(task.Attempts) == 0 {
			continue
		}
		attempt := task.Attempts[len(task.Attempts)-1]
		q := attempt.Question
		if attempt.State != routing.GraphTaskWaitingInput || q == nil || q.Answer != nil {
			return nil
		}
		target := domain.QuestionIdentity{Delivery: view.Delivery, Task: task.TaskID, Execution: attempt.Execution, QuestionID: q.RequestID, OpenedDigest: records[0].Digest}
		if !validNativeQuestion(target) {
			return nil
		}
		// Use the native graph's prompt, paired with this exact latest execution.
		view.Detail["question"] = q.Prompt
		return &domain.PendingQuestion{Target: target, Prompt: q.Prompt}
	}
	return nil
}
