package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/batuta-ai/console/internal/client"
	"github.com/batuta-ai/console/internal/runtime"
)

func TestMismatchErrNamesHome(t *testing.T) {
	err := mismatchErr("127.0.0.1:9477", "/tmp/other-home")
	if err == nil {
		t.Fatal("expected error")
	}
	s := err.Error()
	for _, sub := range []string{"127.0.0.1:9477", "/tmp/other-home", "-home"} {
		if !strings.Contains(s, sub) {
			t.Fatalf("missing %q in %s", sub, s)
		}
	}
}

func TestEnsureDaemonRejectsForeignHome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	err := ensureDaemon(client.New(srv.URL, "foreign"), runtime.Paths{
		Home:   "/tmp/other-home",
		Listen: "127.0.0.1:9477",
	})
	if err == nil {
		t.Fatal("expected mismatch")
	}
	if !strings.Contains(err.Error(), "/tmp/other-home") {
		t.Fatal(err)
	}
}

func TestEnsureDaemonReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api":"v0"}`))
	}))
	t.Cleanup(srv.Close)
	if err := ensureDaemon(client.New(srv.URL, "ok"), runtime.Paths{}); err != nil {
		t.Fatal(err)
	}
}
