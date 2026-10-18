// Package execution adds protocol facts without replacing the v1 execution boundary.
package execution

import (
	"context"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
)

const Version = "2"

type Completeness string

const (
	Unknown  Completeness = "unknown"
	Partial  Completeness = "partial"
	Complete Completeness = "complete"
)

type UsageSource string

const (
	SourceUnknown    UsageSource = "unknown"
	ProviderReported UsageSource = "provider_reported"
)

type TokenCounts struct {
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
}

// UsageObservation is a cumulative snapshot, not a delta or a cost guarantee.
// A nil count is unknown; an explicit zero must retain its pointer.
type UsageObservation struct {
	Counts       TokenCounts
	Completeness Completeness
	Source       UsageSource
	Profile      string
}

// Envelope keeps one encoded delivery alongside its protocol facts. Data is
// already the client representation and must never be emitted a second time.
type Envelope struct {
	Data             []byte
	Usage            *UsageObservation
	Native           *v1.NativeCompletion
	ProtocolTerminal bool
	Failed           bool
}

// Decoder is an explicit optional capability of a registered v1 decoder.
// The host calls either this interface or v1.Decoder, never both on one attempt.
type Decoder interface {
	DecodeEnvelope([]byte) (Envelope, error)
	NewEnvelopeStream() StreamDecoder
}

type StreamDecoder interface {
	DecodeEnvelopes([]byte) ([]Envelope, error)
	EndEnvelopes() ([]Envelope, error)
}

// AttemptFacts combines codec facts with host-owned execution and delivery
// observations. It cannot authorize a request or change accounting.
type AttemptFacts struct {
	Number            int
	Dispatch          v1.Phase
	ProtocolComplete  bool
	DeliveryCommitted bool
	BytesWritten      int64
	Usage             UsageObservation
	Native            v1.NativeCompletion
}

type Outcome struct {
	Legacy   v1.Outcome
	Attempts []AttemptFacts
}

type Executor interface {
	Execute(context.Context, v1.Output, v1.Invocation) Outcome
}
