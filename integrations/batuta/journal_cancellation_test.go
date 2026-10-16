package batuta

import (
	"context"
	"errors"
	"testing"

	"github.com/batuta-ai/console/internal/domain"
)

func TestApprovedProjectionPreservesCancellationAfterCapture(t *testing.T) {
	root, b, a := approvedProjectionFixture(t)
	plan, snapshot, err := approvedDeliverySnapshot(context.Background(), root, b, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, errors.New("cancelled after capture")} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		view, err := projectApprovedSnapshot(ctx, root, b, plan, snapshot)
		if !errors.Is(err, cause) || view.Available || view.Source != domain.SourceUnavailable || view.Progression != nil {
			t.Errorf("cancelled projection lost cause or exposed snapshot: %+v, %v; want %v", view, err, cause)
		}
	}
}
