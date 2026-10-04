//go:build darwin || linux

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestCapabilitiesUnavailablePTY(t *testing.T) {
	const helper = "BATUTA_CAPABILITIES_UNAVAILABLE_TEST"
	if os.Getenv(helper) == "1" {
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		limit.Cur = 0
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/v0/capabilities", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		(&Server{Token: "test-token"}).Handler().ServeHTTP(w, req)
		var got capabilityResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.PTY.Supported || got.PTY.Available || got.Features["pty"] || got.PTY.Code != "pty_unavailable" {
			t.Fatalf("%s", w.Body.String())
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestCapabilitiesUnavailablePTY$")
	cmd.Env = []string{helper + "=1"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated unavailable HTTP handler: %v\n%s", err, output)
	}
}

// The limit is changed only in an isolated child, after the HTTP connection is accepted.
func TestCapabilitiesUnavailableHTTP(t *testing.T) {
	const helper = "BATUTA_CAPABILITIES_UNAVAILABLE_HTTP_TEST"
	if os.Getenv(helper) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestCapabilitiesUnavailableHTTP$")
		cmd.Env = []string{helper + "=1"}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated unavailable HTTP: %v\n%s", err, output)
		}
		return
	}
	handler := (&Server{Token: "test-token"}).Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var original syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		limited := original
		limited.Cur = 0
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limited); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer ts.Close()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v0/capabilities", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got capabilityResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !got.PTY.Supported || got.PTY.Available || got.Features["pty"] || got.PTY.Code != "pty_unavailable" {
		t.Fatalf("HTTP %d: %+v", resp.StatusCode, got.PTY)
	}
}
