// Package execution adds optional token breakdowns without replacing v1/v2 facts.
package execution

import (
	"context"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

const Version = "3"

type TokenBreakdown struct {
	CachedInputTokens     *int64
	CacheWriteInputTokens *int64
	ReasoningOutputTokens *int64
}

// Counts are cumulative snapshots. Nil is unknown; zero remains an owned count.
type UsageObservation struct {
	Counts       v2.TokenCounts
	Breakdown    TokenBreakdown
	Completeness v2.Completeness
	Source       v2.UsageSource
	Profile      string
}

// Execution contains the single wire delivery and the explicit v2 projection.
// Usage has the same base counts plus optional breakdowns, not another delivery.
type Envelope struct {
	Execution v2.Envelope
	Usage     *UsageObservation
}

type Decoder interface {
	DecodeUsageEnvelope([]byte) (Envelope, error)
	NewUsageEnvelopeStream() StreamDecoder
}

type StreamDecoder interface {
	DecodeUsageEnvelopes([]byte) ([]Envelope, error)
	EndUsageEnvelopes() ([]Envelope, error)
}

type AttemptUsage struct {
	Number int
	Usage  UsageObservation
}

type Outcome struct {
	Execution v2.Outcome
	Usage     []AttemptUsage
}

type Executor interface {
	Execute(context.Context, v1.Output, v1.Invocation) Outcome
}
