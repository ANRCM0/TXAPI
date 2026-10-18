// Package summary is an independent L3 consumer of the versioned foundation.
package summary

import (
	"context"
	"errors"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

const Operation execution.Operation = "example.summary"

type Service struct {
	Authority identity.Authority
	Executor  execution.Executor
}

// Run refreshes current permission for every generation; a copied principal or
// the identity from a preceding result cannot authorize a new call.
func (s Service) Run(ctx context.Context, output execution.Output, body []byte) (execution.Outcome, error) {
	if s.Authority == nil || s.Executor == nil {
		return execution.Outcome{}, errors.New("summary dependencies are required")
	}
	principal, ok := s.Authority.Current(ctx)
	if !ok {
		return execution.Outcome{}, identity.ErrUnauthorized
	}
	current, _, err := s.Authority.Refresh(ctx, identity.Binding{UserID: principal.UserID, TokenID: principal.TokenID})
	if err != nil {
		return execution.Outcome{}, err
	}
	return s.Executor.Execute(current, output, execution.Invocation{Operation: Operation, Body: body}), nil
}
