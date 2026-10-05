package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

type questionTransport func(*http.Request) (*http.Response, error)

func (f questionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQuestionClientPreservesEffects(t *testing.T) {
	req := domain.AnswerRequest{MissionID: "m", WorkspaceID: "w", Delivery: "run", Task: "task_1", Execution: 2, QuestionID: "q", OpenedDigest: "o", Text: "answer"}
	for _, tc := range []struct {
		name   string
		status int
		effect domain.AnswerEffect
		code   string
	}{{"success", 200, domain.AnswerRecorded, ""}, {"conflict", 409, domain.AnswerRejected, "answer_stale"}, {"recorded error", 500, domain.AnswerRecorded, "answer_recorded_error"}, {"uncertain", 503, domain.AnswerUncertain, "answer_uncertain"}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			want := domain.AnswerResult{Target: req.Identity(), Effect: tc.effect}
			if tc.effect == domain.AnswerRecorded {
				want.Delivery = req.Delivery
			}
			if tc.code != "" {
				want.Error = &domain.AnswerFailure{Code: tc.code, Message: "details"}
			}
			c := New("http://console.test", "secret")
			c.HTTP.Transport = questionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v0/watch/answer" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Fatalf("request %v", r)
				}
				var got domain.AnswerRequest
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != req {
					t.Fatalf("body %+v %v", got, err)
				}
				b, _ := json.Marshal(want)
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(string(b))), Header: make(http.Header)}, nil
			})
			got, err := c.AnswerQuestion(context.Background(), req)
			if got.Effect != want.Effect || got.Target != want.Target || got.Delivery != want.Delivery || calls != 1 {
				t.Fatalf("lost evidence %+v calls=%d", got, calls)
			}
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var failure *AnswerError
				if !errors.As(err, &failure) || failure.Status != tc.status || failure.Result.Error.Code != tc.code {
					t.Fatalf("typed error %v", err)
				}
			}
		})
	}
}

func TestQuestionClientTransportUncertaintyAndCancellation(t *testing.T) {
	req := domain.AnswerRequest{MissionID: "m", WorkspaceID: "w", Delivery: "run"}
	for _, mode := range []string{"network", "canceled in transport", "malformed", "truncated", "wrong target", "unauthorized", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c := New("http://console.test", "secret")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.HTTP.Transport = questionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				switch mode {
				case "network":
					return nil, io.ErrUnexpectedEOF
				case "canceled in transport":
					cancel()
					return nil, r.Context().Err()
				}
				status := 200
				body := `{`
				switch mode {
				case "truncated":
					body = `{"effect":"recorded"}`
				case "wrong target":
					body = `{"effect":"recorded","delivery":"run","target":{"mission_id":"other"}}`
				case "unauthorized":
					status = 401
					body = `{"error":"unauthorized","message":"authentication required"}`
				case "redirect":
					status = 307
					body = ""
				}
				h := make(http.Header)
				if mode == "redirect" {
					h.Set("Location", "http://console.test/another")
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			result, err := c.AnswerQuestion(ctx, req)
			effect := domain.AnswerUncertain
			if mode == "unauthorized" {
				effect = domain.AnswerRejected
			}
			if err == nil || result.Effect != effect || result.Target != req.Identity() || calls != 1 {
				t.Fatalf("result %+v error=%v calls=%d", result, err, calls)
			}
			if mode == "canceled in transport" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
	c := New("http://console.test", "secret")
	c.HTTP.Transport = questionTransport(func(*http.Request) (*http.Response, error) { t.Fatal("sent canceled request"); return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := c.AnswerQuestion(ctx, req)
	if !errors.Is(err, context.Canceled) || result.Effect != domain.AnswerRejected {
		t.Fatalf("preflight cancellation %+v %v", result, err)
	}
}
