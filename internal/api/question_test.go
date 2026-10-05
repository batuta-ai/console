package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/service"
	"github.com/batuta-ai/console/internal/store"
	"github.com/batuta-ai/core/journal"
	"github.com/batuta-ai/core/loop"
	"github.com/batuta-ai/core/routing"
)

func answerHTTPFixture(t *testing.T) (http.Handler, *service.Service, *journal.Store, domain.AnswerRequest, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "catalog.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); db.Close() })
	svc := service.New(st, nil, nil)
	root := t.TempDir()
	path := bindingFixture(t, root, "alpha", "run")
	if err := st.PutWorkspace(domain.Workspace{ID: "w", Root: root, CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMission(domain.Mission{ID: "m", WorkspaceID: "w", CreatedAt: domain.Now()}); err != nil {
		t.Fatal(err)
	}
	m, err := svc.BindMission("m", service.MissionBindingRequest{WorkspaceID: "w", PlanPath: path, DeliveryID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	js, err := journal.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	qid := "sha256:" + strings.Repeat("a", 64)
	attempt := routing.GraphTaskAttempt{Execution: 1, State: routing.GraphTaskWaitingInput, ChildRunID: "run", WorktreeID: "wt", WorktreeRoot: root, BaseHeadSHA: strings.Repeat("b", 40), Runtime: routing.RuntimeValue{Provider: "codex", Model: "fixture", Reasoning: "medium"}, Question: &routing.TaskQuestion{RequestID: qid, Prompt: "Choose", ContextDigest: "sha256:" + strings.Repeat("c", 64)}}
	graph, _ := json.Marshal(routing.DeliveryGraph{Tasks: []routing.GraphTask{{TaskID: "task_1", State: routing.GraphTaskWaitingInput, Attempts: []routing.GraphTaskAttempt{attempt}}}})
	if _, err := js.Append("run", journal.Record{Kind: loop.KindQuestion, TaskID: "task_1", Graph: graph, Detail: []byte(`{"execution":1,"prompt":"Choose"}`)}); err != nil {
		t.Fatal(err)
	}
	req := domain.AnswerRequest{MissionID: "m", WorkspaceID: "w", Delivery: "run", Task: "task_1", Execution: 1, QuestionID: qid, OpenedDigest: m.Binding.OpenedDigest, Text: "answer"}
	return (&Server{Svc: svc, Token: "test-token"}).Handler(), svc, js, req, db
}

func TestQuestionHTTPRejectsWithoutMutation(t *testing.T) {
	for _, mode := range []string{"unauthorized", "trailing", "malformed", "unknown field", "missing", "foreign", "stale", "canceled", "store failure"} {
		t.Run(mode, func(t *testing.T) {
			h, svc, js, req, _ := answerHTTPFixture(t)
			status := 400
			switch mode {
			case "missing":
				req.Execution = 0
			case "foreign":
				req.MissionID = "absent"
				status = 404
			case "stale":
				req.Execution = 2
				status = 409
			case "canceled":
				status = 408
			case "store failure":
				if err := svc.Store.Close(); err != nil {
					t.Fatal(err)
				}
				status = 500
			}
			body, _ := json.Marshal(req)
			switch mode {
			case "trailing":
				body = append(body, []byte(` {}`)...)
			case "malformed":
				body = []byte(`{"mission_id":`)
			case "unknown field":
				body = append(body[:len(body)-1], []byte(`,"unknown":true}`)...)
			}
			r := httptest.NewRequest(http.MethodPost, "/v0/watch/answer", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer test-token")
			if mode == "unauthorized" {
				r.Header.Del("Authorization")
				status = 401
			}
			if mode == "canceled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			before, err := os.ReadFile(js.Path("run"))
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != status {
				t.Errorf("status %d want %d: %s", w.Code, status, w.Body.String())
			}
			after, _ := os.ReadFile(js.Path("run"))
			if string(after) != string(before) {
				t.Fatal("rejected HTTP request mutated journal")
			}
			if mode != "unauthorized" {
				var result domain.AnswerResult
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Effect != domain.AnswerRejected || result.Error == nil {
					t.Fatalf("lost typed rejection: %s %v", w.Body.String(), err)
				}
			}
		})
	}
}
func TestQuestionHTTPRecordedAndCatalogFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "catalog failure"}[fail], func(t *testing.T) {
			h, _, js, req, db := answerHTTPFixture(t)
			if fail {
				if _, err := db.Exec(`CREATE TRIGGER reject_answer BEFORE INSERT ON events WHEN NEW.kind='watch.answered' BEGIN SELECT RAISE(ABORT,'event rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			status := 200
			if fail {
				status = 500
			}
			raw := post(t, h, "/v0/watch/answer", req, status)
			b, _ := json.Marshal(raw)
			var result domain.AnswerResult
			_ = json.Unmarshal(b, &result)
			if result.Effect != domain.AnswerRecorded || result.Delivery != "run" || result.Target != req.Identity() || (result.Error != nil) != fail {
				t.Fatalf("lost accepted effect %+v", result)
			}
			records, err := js.Read("run")
			if err != nil || len(records) != 3 || records[2].Kind != loop.KindAnswer {
				t.Fatalf("native effect %v %v", records, err)
			}
			raw = post(t, h, "/v0/watch/answer", req, 409)
			if raw["effect"] != "rejected" {
				t.Fatalf("duplicate %v", raw)
			}
		})
	}
}

type answerHandlerTransport struct{ handler http.Handler }

func (tr answerHandlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	tr.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestQuestionTypedClientThroughAuthenticatedHandler(t *testing.T) {
	for _, mode := range []string{"success", "recorded error", "uncertain", "owned", "unauthorized", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			h, _, js, req, db := answerHTTPFixture(t)
			if mode == "recorded error" {
				if _, err := db.Exec(`CREATE TRIGGER reject_answer BEFORE INSERT ON events WHEN NEW.kind='watch.answered' BEGIN SELECT RAISE(ABORT,'event rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "uncertain" {
				records, err := js.Read("run")
				if err != nil {
					t.Fatal(err)
				}
				last := records[len(records)-1]
				var graph routing.DeliveryGraph
				if err := json.Unmarshal(last.Graph, &graph); err != nil {
					t.Fatal(err)
				}
				graph.Tasks[0].Attempts = append(graph.Tasks[0].Attempts, graph.Tasks[0].Attempts[0])
				last.Graph, _ = json.Marshal(graph)
				if _, err := js.Append("run", last); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "owned" {
				host, err := os.Hostname()
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "host": host, "started_at": time.Now().UTC(), "refreshed_at": time.Now().UTC().Add(time.Hour)})
				if err := os.WriteFile(strings.TrimSuffix(js.Path("run"), ".jsonl")+".lock", b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c := client.New("http://console.test", "test-token")
			c.HTTP.Transport = answerHandlerTransport{h}
			if mode == "unauthorized" {
				c.Token = "wrong"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			before, err := js.Read("run")
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.AnswerQuestion(ctx, req)
			want := domain.AnswerRecorded
			if mode == "uncertain" {
				want = domain.AnswerUncertain
			}
			if mode == "owned" || mode == "unauthorized" || mode == "canceled" {
				want = domain.AnswerRejected
			}
			if result.Effect != want || result.Target != req.Identity() || (err != nil) != (mode != "success") {
				t.Fatalf("effect %+v %v", result, err)
			}
			if mode == "owned" {
				var answerErr *client.AnswerError
				if !errors.As(err, &answerErr) || answerErr.Status != 409 || answerErr.Result.Error.Code != "answer_owned" {
					t.Fatalf("owned error %v", err)
				}
			}
			after, readErr := js.Read("run")
			if readErr != nil {
				t.Fatal(readErr)
			}
			added := 0
			if want == domain.AnswerRecorded {
				added = 1
			}
			if len(after) != len(before)+added {
				t.Fatalf("native effects: %d -> %d", len(before), len(after))
			}
			if want == domain.AnswerRecorded {
				again, err := c.AnswerQuestion(ctx, req)
				var answerErr *client.AnswerError
				if !errors.As(err, &answerErr) || answerErr.Status != 409 || again.Effect != domain.AnswerRejected {
					t.Fatalf("duplicate %+v %v", again, err)
				}
			}
		})
	}
}

// Kept as native listener qualification for the conductor's unrestricted host.
func TestQuestionNativeHTTPClient(t *testing.T) {
	h, _, js, req, _ := answerHTTPFixture(t)
	server := httptest.NewServer(h)
	defer server.Close()
	c := client.New(server.URL, "test-token")
	var view domain.WatchView
	if err := c.Get("/v0/watch?mission=m&workspace=w", &view); err != nil {
		t.Fatal(err)
	}
	if view.PendingQuestion == nil || view.PendingQuestion.Target != req.Identity() {
		t.Fatalf("watch round trip %+v", view)
	}
	result, err := c.AnswerQuestion(context.Background(), view.PendingQuestion.Target.Answer("native client"))
	if err != nil || result.Effect != domain.AnswerRecorded {
		t.Fatalf("answer %+v %v", result, err)
	}
	records, err := js.Read("run")
	if err != nil || len(records) != 3 || records[2].Kind != loop.KindAnswer {
		t.Fatalf("journal %v %v", records, err)
	}
}
