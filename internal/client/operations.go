package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/batuta-ai/console/internal/domain"
)

type OperationEffect string

const (
	EffectNotDispatched OperationEffect = "not_dispatched"
	EffectUncertain     OperationEffect = "uncertain"
	EffectRejected      OperationEffect = "rejected"
	EffectSucceeded     OperationEffect = "succeeded"
	EffectReadOnly      OperationEffect = "read_only"
)

// OperationError never treats a mutating server failure as proof of no effect.
type OperationError struct {
	Status  int
	Code    string
	Message string
	Effect  OperationEffect
	Cause   error
}

func (e *OperationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("console: %v", e.Cause)
	}
	return e.Code + ": " + e.Message
}
func (e *OperationError) Unwrap() error { return e.Cause }

const maxOperationBytes = 16 << 20

func resourcePath(collection, id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return "", domain.ErrInvalidReference
	}
	return "/v0/" + collection + "/" + url.PathEscape(id), nil
}

func (c *Client) operation(ctx context.Context, method, path string, body, out any) error {
	effect := EffectReadOnly
	mutation := method != http.MethodGet
	if mutation {
		effect = EffectNotDispatched
	}
	fail := func(status int, cause error) error {
		return &OperationError{Status: status, Effect: effect, Cause: cause}
	}
	if err := ctx.Err(); err != nil {
		return fail(0, err)
	}
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fail(0, err)
		}
	}
	if len(payload) > maxOperationBytes {
		return fail(0, fmt.Errorf("request exceeds limit"))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(payload))
	if err != nil {
		return fail(0, err)
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := ctx.Err(); err != nil {
		return fail(0, err)
	}
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if mutation {
		effect = EffectUncertain
	}
	res, err := hc.Do(req)
	if err != nil {
		return fail(0, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxOperationBytes+1))
	if err != nil {
		return fail(res.StatusCode, err)
	}
	if len(data) > maxOperationBytes {
		return fail(res.StatusCode, fmt.Errorf("response exceeds limit"))
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		failure := &OperationError{Status: res.StatusCode, Effect: effect}
		var wire struct {
			Code    string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			failure.Cause = err
		} else {
			failure.Code = wire.Code
			failure.Message = wire.Message
		}
		// The authentication middleware rejects before calling any operation handler.
		if mutation && res.StatusCode == http.StatusUnauthorized && wire.Code == "unauthorized" {
			failure.Effect = EffectRejected
		}
		return failure
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fail(res.StatusCode, err)
	}
	return nil
}

func operationResult[T any](c *Client, ctx context.Context, method, path string, body any) (T, error) {
	var out T
	err := c.operation(ctx, method, path, body, &out)
	return out, err
}
func resourceResult[T any](c *Client, ctx context.Context, method, collection, id, suffix string, body any) (T, error) {
	path, err := resourcePath(collection, id)
	if err != nil {
		var out T
		effect := EffectNotDispatched
		if method == http.MethodGet {
			effect = EffectReadOnly
		}
		return out, &OperationError{Effect: effect, Cause: err}
	}
	return operationResult[T](c, ctx, method, path+suffix, body)
}
