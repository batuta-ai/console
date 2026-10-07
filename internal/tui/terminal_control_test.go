package tui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/domain"
)

type terminalRoundTrip func(*http.Request) (*http.Response, error)

func (f terminalRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTerminalControlRedirectNoReplay(t *testing.T) {
	var recipient atomic.Int32
	listener := &terminalPipeListener{incoming: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/sessions/selected/control" {
			http.Redirect(w, r, "/replayed-control", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/replayed-control" {
			recipient.Add(1)
			json.NewEncoder(w).Encode(domain.Session{ID: "selected", Owner: "tui", OwnerClient: "actual"})
			return
		}
		http.NotFound(w, r)
	})}
	joined := make(chan struct{})
	go func() { defer close(joined); server.Serve(listener) }()
	transport := &http.Transport{DialContext: listener.dial}
	defer func() { server.Close(); listener.Close(); transport.CloseIdleConnections(); <-joined }()
	c := client.New("http://terminal-fixture", "disposable-root-probe")
	c.HTTP = &http.Client{Transport: transport}
	l := &termLink{api: c, id: "selected", clientID: "actual"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := l.control(ctx, "tui")
	if recipient.Load() != 0 {
		t.Errorf("control mutation replayed to redirect recipient %d times", recipient.Load())
	}
	if err == nil {
		t.Error("redirect control was accepted instead of reporting uncertainty")
	}
}
