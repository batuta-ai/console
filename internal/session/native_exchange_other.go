//go:build !windows

package session

import (
	"context"
	"io"

	"github.com/batuta-ai/core/publication"
)

func (r NativeRunner) runNativeExchange(context.Context, publication.Command, func(context.Context, io.Reader, io.WriteCloser) error, func(NativeExchangeFinalization, io.Reader, io.WriteCloser) error) (publication.CommandResult, error) {
	return publication.CommandResult{ExitCode: -1}, ErrProbeUnsupported
}
