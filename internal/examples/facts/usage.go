package facts

import (
	"context"
	"errors"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	identity "minigateway/internal/foundation/contracts/identity/v1"
)

// UsageService consumes optional quantities without granting financial authority.
type UsageService struct {
	Authority identity.Authority
	Executor  v3.Executor
}

func (s UsageService) Run(ctx context.Context, output v1.Output, body []byte) (v3.Outcome, error) {
	if s.Authority == nil || s.Executor == nil {
		return v3.Outcome{}, errors.New("usage example dependencies are required")
	}
	current, err := currentContext(ctx, s.Authority)
	if err != nil {
		return v3.Outcome{}, err
	}
	return s.Executor.Execute(current, output, v1.Invocation{Operation: v1.Responses, Body: body}), nil
}
