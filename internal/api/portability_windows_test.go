package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestWindowsCapabilitiesSelectedWithPendingQualification(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	h := (&Server{Token: "synthetic-windows-token"}).Handler()
	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v0/capabilities", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", unauthorized.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/v0/capabilities", nil)
	req.Header.Set("Authorization", "Bearer synthetic-windows-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
	}
	var got capabilityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Runtime.OS != "windows" || !got.Runtime.Selected || !reflect.DeepEqual(got.Matrix.Platforms, []string{"darwin", "linux", "windows"}) {
		t.Fatalf("Windows not selected: %+v %+v", got.Runtime, got.Matrix)
	}
	if !got.PTY.Available || !got.PTY.Supported || got.PTY.Code != "pty_available" || !got.Features["pty"] || got.Features["account_fallback"] || got.PTY.Qualification.Status != "pending" || got.Matrix.Qualification.Status != "pending" {
		t.Fatalf("unsafe capabilities: %+v %+v", got.PTY, got.Features)
	}
	if len(got.Providers) == 0 {
		t.Fatal("missing provider diagnostics")
	}
	for _, p := range got.Providers {
		if p.Installation.Status != "missing" || p.AccountReadiness.Status != "not_checked" {
			t.Fatalf("unsafe provider capability: %+v", p)
		}
		if p.Selected {
			if !p.Supported || p.Qualification.Status != "pending" || p.Qualification.Code != "native_operation_qualification_pending" || p.Compatibility.Status != "not_checked" || p.Compatibility.Code != "isolated_account_required" {
				t.Fatalf("wrong selected provider observation: %+v", p)
			}
		} else if p.Supported || p.Qualification.Status != "unsupported" || p.Compatibility.Status != "unsupported" {
			t.Fatalf("unsafe unsupported provider capability: %+v", p)
		}
	}
}
