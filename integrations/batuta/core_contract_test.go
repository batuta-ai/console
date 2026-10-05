package batuta

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
)

func TestPublishedQuestionInvalidTargetDoesNotMutateJournal(t *testing.T) {
	root := t.TempDir()
	store, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.Append("contract-delivery", journal.Record{
		Kind:   loop.KindOpened,
		At:     time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Graph:  []byte(`{"tasks":[],"waves":[]}`),
		Detail: []byte(`{"slug":"contract"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, journal.Dir, "contract-delivery.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, journal.Dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		invalidate func(*loop.QuestionTarget)
	}{
		{"delivery", func(q *loop.QuestionTarget) { q.DeliveryID = "../escape" }},
		{"task", func(q *loop.QuestionTarget) { q.TaskID = "" }},
		{"execution", func(q *loop.QuestionTarget) { q.Execution = 0 }},
		{"question", func(q *loop.QuestionTarget) { q.QuestionID = "invalid" }},
		{"opening", func(q *loop.QuestionTarget) { q.OpenedDigest = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := loop.QuestionTarget{DeliveryID: "contract-delivery", TaskID: "task_1", Execution: 1,
				QuestionID: "sha256:" + strings.Repeat("a", 64), OpenedDigest: opened.Digest}
			tc.invalidate(&target)
			id, err := loop.AnswerQuestion(context.Background(), root, target, "consumer answer")
			if id != "" || !errors.Is(err, loop.ErrInvalidQuestionTarget) {
				t.Fatalf("invalid target: delivery=%q error=%v", id, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("invalid target mutated journal bytes")
			}
			afterEntries, err := os.ReadDir(filepath.Join(root, journal.Dir))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(entries, afterEntries) {
				t.Fatal("invalid target changed journal directory")
			}
		})
	}
}
