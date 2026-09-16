package batuta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

const API = "v0"

// Project maps a Batuta journal through loop.PanelModel. The journal stays
// the authority for deliveries. This does not start the watch TUI and does
// not call a model.
func Project(workspace, delivery string) (domain.WatchView, error) {
	out := domain.WatchView{API: API, Workspace: workspace, Available: false}
	if workspace == "" {
		out.Message = "workspace root required"
		return out, nil
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return out, err
	}
	if _, err := os.Stat(filepath.Join(abs, ".batuta", "journal")); err != nil {
		out.Message = "no batuta journal in this workspace"
		return out, nil
	}
	store, err := journal.Open(abs)
	if err != nil {
		return out, err
	}
	if delivery == "" {
		ids, err := store.List()
		if err != nil {
			return out, err
		}
		for _, id := range ids {
			records, err := store.Read(id)
			if err != nil || len(records) == 0 {
				continue
			}
			delivery = id
			break
		}
	}
	if delivery == "" {
		out.Message = "no deliveries"
		return out, nil
	}
	records, err := store.Read(delivery)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	view := loop.PanelModel(records, now, "")
	presence, loops := loop.Presence(abs, delivery, now)
	view.Header.Presence = presence
	view.Header.Loops = loops
	return dto(abs, delivery, view), nil
}

func dto(workspace, delivery string, view loop.PanelView) domain.WatchView {
	waves := make([]map[string]any, 0, len(view.Waves))
	for _, w := range view.Waves {
		rows := make([]map[string]any, 0, len(w.Rows))
		for _, r := range w.Rows {
			rows = append(rows, map[string]any{
				"task":    r.Task,
				"title":   r.Title,
				"state":   r.State,
				"attempt": r.Attempt,
				"gates":   []string{r.Gates[0], r.Gates[1], r.Gates[2], r.Gates[3]},
				"commit":  r.Commit,
			})
		}
		waves = append(waves, map[string]any{
			"number": w.Number, "state": w.State, "done": w.Done, "total": w.Total,
			"base": w.Base, "integrated": w.Integrated, "rows": rows, "dependencies": w.Dependencies,
		})
	}
	return domain.WatchView{
		API: API, Workspace: workspace, Delivery: delivery, Available: true,
		Header: map[string]any{
			"presence": view.Header.Presence, "loops": view.Header.Loops, "delivery": view.Header.Delivery,
			"project": view.Header.Project, "branch": view.Header.Branch, "head": view.Header.Head,
			"state": view.Header.State, "roadmap": view.Header.Roadmap, "phase": view.Header.Phase,
			"phase_title": view.Header.PhaseTitle,
		},
		Attention: map[string]any{"kind": view.Attention.Kind, "task": view.Attention.Task, "text": view.Attention.Text, "hint": view.Attention.Hint},
		Context: map[string]any{
			"executor": view.Context.Executor, "model": view.Context.Model, "reasoning": view.Context.Reasoning,
			"sessions": view.Context.Sessions, "retries": view.Context.Retries, "escalations": view.Context.Escalations, "pid": view.Context.PID,
		},
		Progress: map[string]any{
			"criterion": view.Progress.Criterion, "criterion_total": view.Progress.CriterionTotal, "criterion_title": view.Progress.CriterionTitle,
			"waves_done": view.Progress.WavesDone, "waves_total": view.Progress.WavesTotal, "tasks_done": view.Progress.TasksDone, "tasks_total": view.Progress.TasksTotal,
		},
		Detail: map[string]any{
			"task": view.Detail.Task, "title": view.Detail.Title, "attempt": view.Detail.Attempt,
			"criterion": view.Detail.Criterion, "criterion_state": view.Detail.CriterionState,
			"question": view.Detail.Question, "reason": view.Detail.Reason, "last": view.Detail.LastRecord,
			"worktree": view.Detail.Worktree, "log_path": view.Detail.LogPath,
		},
		Waves: waves,
		Health: map[string]any{
			"journal_age_ns": view.Health.JournalAge.Nanoseconds(), "tree_dirty": view.Health.TreeDirty, "last_error": view.Health.LastError,
		},
		LogTitle: view.LogTitle, LogLines: view.LogLines,
	}
}

type AnswerRequest struct {
	Workspace  string `json:"workspace"`
	Delivery   string `json:"delivery,omitempty"`
	Task       string `json:"task"`
	QuestionID string `json:"question_id,omitempty"`
	Text       string `json:"text"`
}

// Answer delegates to loop.Answer / loop.AnswerDelivery. It never writes
// journal files itself; core remains the delivery authority.
func Answer(req AnswerRequest) (string, error) {
	if req.Workspace == "" || req.Text == "" {
		return "", fmt.Errorf("workspace and text are required")
	}
	abs, err := filepath.Abs(req.Workspace)
	if err != nil {
		return "", err
	}
	if req.Delivery != "" && req.QuestionID != "" {
		return loop.AnswerDelivery(abs, req.Delivery, req.Task, req.QuestionID, req.Text)
	}
	return loop.Answer(abs, req.Task, req.Text)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
