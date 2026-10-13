package session

import (
	"context"
	"io"

	"github.com/batuta-ai/core/publication"
)

// Exchange reserves stdin/stdout for a bidirectional protocol. The handler must
// return after its context is cancelled or its streams terminate.
func (r NativeRunner) Exchange(ctx context.Context, command publication.Command, handler func(context.Context, io.Reader, io.WriteCloser) error) (publication.CommandResult, error) {
	return r.runNativeExchange(ctx, command, handler)
}
