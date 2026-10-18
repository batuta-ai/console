package session

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/batuta-ai/core/publication"
)

// Exchange reserves stdin/stdout for a bidirectional protocol. The handler must
// return after its context is cancelled or its streams terminate.
func (r NativeRunner) Exchange(ctx context.Context, command publication.Command, handler func(context.Context, io.Reader, io.WriteCloser) error) (publication.CommandResult, error) {
	return r.runNativeExchange(ctx, command, handler, nil)
}

// NativeExchangeFinalization separates owned control cancellation from cleanup.
// ProtocolDone must be called once the final message passes validation, before
// acknowledging it or draining the streams. It never grants process authority.
type NativeExchangeFinalization struct {
	Control      context.Context
	Cleanup      context.Context
	ProtocolDone func()
}

var ErrNativeExchangeExpired = errors.New("native exchange finalization expired")

// NativeExchangeExitError reports an observed nonzero exit after all joins,
// only when the owned process was not forcibly terminated.
type NativeExchangeExitError struct{ Code int }

func (e *NativeExchangeExitError) Error() string {
	return fmt.Sprintf("native exchange exited %d", e.Code)
}

// ExchangeFinalizing gives the Core helper five seconds from owned control
// cancellation and two seconds from validated protocol completion, whichever
// deadline is earlier. Neither deadline releases ownership before all joins.
func (r NativeRunner) ExchangeFinalizing(ctx context.Context, command publication.Command, handler func(NativeExchangeFinalization, io.Reader, io.WriteCloser) error) (publication.CommandResult, error) {
	return r.runNativeExchange(ctx, command, nil, handler)
}
