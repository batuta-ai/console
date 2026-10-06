package service

import (
	"context"
	"errors"
	"os"

	"github.com/batuta-ai/console/internal/domain"
	"github.com/batuta-ai/console/internal/session"
	"github.com/batuta-ai/core/publication"
	"github.com/batuta-ai/core/review"
	"github.com/batuta-ai/core/routing"
)

type nativeReviewRunner struct {
	runner   session.NativeRunner
	accounts nativeAccounts
	guard    func(context.Context) error
	cleanup  func(error)
}

func (r nativeReviewRunner) Run(ctx context.Context, command publication.Command) (publication.CommandResult, error) {
	if err := r.guard(ctx); err != nil {
		return publication.CommandResult{ExitCode: -1}, err
	}
	if len(command.Args) > 0 && command.Args[0] == "review" && !(len(command.Args) == 2 && command.Args[1] == "-h") {
		root, err := os.OpenRoot(command.Directory)
		if err != nil {
			return publication.CommandResult{ExitCode: -1}, err
		}
		bytes, readErr := root.ReadFile(".batuta/routing.md")
		err = errors.Join(readErr, root.Close())
		if err != nil || len(bytes) > 1<<20 {
			return publication.CommandResult{ExitCode: -1}, errors.Join(domain.ErrInvalidReference, err)
		}
		table, err := routing.ParseRoutingTable(bytes)
		if err != nil {
			return publication.CommandResult{ExitCode: -1}, err
		}
		runtime, err := review.ReviewerRuntimeFromTable(table, routing.DomainGeneral, "")
		account, bound := r.accounts.byCLI[runtime.Provider]
		if err != nil || !bound || !account.permits(runtime.Model, runtime.Reasoning) {
			return publication.CommandResult{ExitCode: -1}, errors.Join(domain.ErrInvalidReference, err)
		}
	}
	result, err := r.runner.Run(ctx, command)
	if errors.Is(err, session.ErrNativeCleanupUnresolved) {
		err = errors.Join(publication.ErrReviewCleanupUnresolved, err)
		r.cleanup(err)
	}
	return result, err
}
