package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestNamedAccountContract(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v0/accounts" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"selected","label":"second","adapter":"codex","isolation":"not_yet_qualified"}`))
	}))
	defer s.Close()
	got, err := New(s.URL, "secret").CreateAccount(context.Background(), CreateAccountRequest{Label: "second", Adapter: "codex"})
	if err != nil || got.ID != "selected" || got.Isolation != domain.IsolationNotYetQualified {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOperationPreflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New("http://invalid", "secret").CreateAccount(ctx, CreateAccountRequest{})
	var failure *OperationError
	if !errors.As(err, &failure) || !errors.Is(err, context.Canceled) || failure.Effect != EffectNotDispatched {
		t.Fatalf("%v", err)
	}
}

func TestEventCursorPreflight(t *testing.T) {
	cursor := int64(-1)
	_, err := New("http://invalid", "secret").DialEvents(context.Background(), &cursor)
	if !errors.Is(err, domain.ErrInvalidReference) {
		t.Fatalf("%v", err)
	}
}
