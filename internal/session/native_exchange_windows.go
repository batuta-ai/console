package session

import (
	"context"
	"errors"
	"io"

	"github.com/batuta-ai/core/publication"
)

func (r NativeRunner) runNativeExchange(context.Context, publication.Command, func(context.Context, io.Reader, io.WriteCloser) error) (publication.CommandResult, error) {
	return publication.CommandResult{ExitCode: -1}, errors.ErrUnsupported
}
