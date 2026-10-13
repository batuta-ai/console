//go:build windows

package session

import (
	"context"
	"os/exec"
	"testing"
)

func TestWindowsPTYObservationKeepsLegacyStartBlocked(t *testing.T) {
	got := ObservePTY(context.Background())
	if !got.Supported || !got.Available || got.Code != "pty_available" || got.Qualification.Status != "pending" || got.Qualification.Code != "native_operation_qualification_pending" {
		t.Fatalf("%+v", got)
	}
	if f, err := startPTY(exec.Command("cmd"), 80, 24); err == nil || f != nil {
		t.Fatal("Windows start must remain blocked")
	}
	if err := resizePTY(nil, 80, 24); err == nil {
		t.Fatal("Windows resize must remain blocked")
	}
}
