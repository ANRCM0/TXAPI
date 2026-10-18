// Package facts is an independent L3 consumer of the additive execution facts port.
package facts

import (
	"context"
	"errors"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

type Service struct {
	Authority identity.Authority
	Executor  v2.Executor
}

// Run refreshes the trusted binding for each generation, including successive
// calls on a long-lived parent context. Facts never serve as authorization.
func (s Service) Run(ctx context.Context, output v1.Output, body []byte) (v2.Outcome, error) {
	if s.Authority == nil || s.Executor == nil {
		return v2.Outcome{}, errors.New("facts example dependencies are required")
	}
	principal, ok := s.Authority.Current(ctx)
	if !ok {
		return v2.Outcome{}, identity.ErrUnauthorized
	}
	current, _, err := s.Authority.Refresh(ctx, identity.Binding{UserID: principal.UserID, TokenID: principal.TokenID})
	if err != nil {
		return v2.Outcome{}, err
	}
	return s.Executor.Execute(current, output, v1.Invocation{Operation: v1.Responses, Body: body}), nil
}
