package client

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeReadyUnauthorizedDown(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api":"v0"}`))
	}))
	defer ok.Close()

	if New(ok.URL, "good").Probe() != StatusReady {
		t.Fatal("matching token should be ready")
	}
	if New(ok.URL, "bad").Probe() != StatusUnauthorized {
		t.Fatal("wrong token should be unauthorized, not down")
	}
	ok.Close()
	if New(ok.URL, "good").Probe() != StatusDown {
		t.Fatal("closed server should be down")
	}
}
