package facts

import (
	"context"
	"errors"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	quotation "minigateway/internal/foundation/contracts/quotation/v1"
)

type QuotationService struct {
	Authority identity.Authority
	Executor  v3.Executor
	Quotes    quotation.Reader
}

type QuotedOutcome struct {
	Execution v3.Outcome
	Quote     quotation.Quote
}

// Run pins a selling version before executing once. An error after Execute is
// observational; callers retain the outcome and must not repeat the generation.
func (s QuotationService) Run(ctx context.Context, output v1.Output, body []byte) (QuotedOutcome, error) {
	if s.Authority == nil || s.Executor == nil || s.Quotes == nil {
		return QuotedOutcome{}, errors.New("quotation example dependencies are required")
	}
	current, err := currentContext(ctx, s.Authority)
	if err != nil {
		return QuotedOutcome{}, err
	}
	version, err := s.Quotes.Current(current)
	if err != nil {
		return QuotedOutcome{}, err
	}
	if !version.Enabled {
		return QuotedOutcome{}, quotation.ErrUnavailable
	}
	out := QuotedOutcome{Execution: s.Executor.Execute(current, output, v1.Invocation{Operation: v1.Responses, Body: body})}
	legacy := out.Execution.Execution.Legacy
	if len(legacy.Attempts) != 1 || len(out.Execution.Usage) != 1 || out.Execution.Usage[0].Number != 1 {
		return out, errors.New("quotation example requires a single trustworthy attempt binding")
	}
	out.Quote, err = s.Quotes.Calculate(current, quotation.Input{PriceVersion: version.Version,
		Model: legacy.Attempts[0].Model, Group: legacy.ChannelGroup, Usage: out.Execution.Usage[0].Usage})
	return out, err
}
