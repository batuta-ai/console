package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/batuta-ai/console/internal/domain"
)

// AnswerError retains the server's evidence even when the HTTP status is an
// error. A transport failure after dispatch is uncertain; refresh before retry.
type AnswerError struct {
	Result domain.AnswerResult
	Status int
	Cause  error
}

func (e *AnswerError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("answer: %v", e.Cause)
	}
	if e.Result.Error != nil {
		return e.Result.Error.Code + ": " + e.Result.Error.Message
	}
	return fmt.Sprintf("answer: HTTP %d", e.Status)
}
func (e *AnswerError) Unwrap() error { return e.Cause }

func (c *Client) AnswerQuestion(ctx context.Context, answer domain.AnswerRequest) (domain.AnswerResult, error) {
	result := domain.AnswerResult{Target: answer.Identity(), Effect: domain.AnswerRejected}
	fail := func(status int, err error) (domain.AnswerResult, error) {
		return result, &AnswerError{Result: result, Status: status, Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(0, err)
	}
	body, err := json.Marshal(answer)
	if err != nil {
		return fail(0, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/v0/watch/answer", bytes.NewReader(body))
	if err != nil {
		return fail(0, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	// Redirecting this mutation could replay an answer outside the shown target.
	httpClient := *c.HTTP
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	result.Effect = domain.AnswerUncertain
	response, err := httpClient.Do(req)
	if err != nil {
		return fail(0, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		result.Effect = domain.AnswerRejected
		result.Error = &domain.AnswerFailure{Code: "answer_unauthorized", Message: "answer authorization failed"}
		return fail(response.StatusCode, nil)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	var received domain.AnswerResult
	if err := decoder.Decode(&received); err != nil {
		return fail(response.StatusCode, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return fail(response.StatusCode, fmt.Errorf("invalid trailing answer response"))
	}
	if received.Target != answer.Identity() ||
		(received.Effect != domain.AnswerRecorded && received.Effect != domain.AnswerRejected && received.Effect != domain.AnswerUncertain) ||
		(received.Effect == domain.AnswerRecorded && received.Delivery != answer.Delivery) ||
		(response.StatusCode >= 300 && received.Error == nil) ||
		(response.StatusCode < 300 && (received.Effect != domain.AnswerRecorded || received.Error != nil)) {
		return fail(response.StatusCode, fmt.Errorf("invalid answer response identity or effect"))
	}
	result = received
	if response.StatusCode >= 300 {
		return fail(response.StatusCode, nil)
	}
	return result, nil
}
