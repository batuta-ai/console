package session

import (
	"context"
	"errors"
	"testing"
)

func TestStopCleanupRetainsOwnershipAndHomeErrors(t *testing.T) {
	m := New(t.TempDir())
	ownershipErr := errors.New("owned group cleanup failed")
	homeErr := errors.New("private home cleanup failed")
	m.recordCleanup(ownershipErr, ownershipErr)
	m.recordCleanup(homeErr, homeErr)
	if err := m.Shutdown(context.Background()); !errors.Is(err, ownershipErr) || !errors.Is(err, homeErr) {
		t.Fatalf("completed cleanup lost a persistent error: %v", err)
	}
}
